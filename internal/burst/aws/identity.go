package aws

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/burst"
	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

// AudienceHeader binds a signed GetCallerIdentity request to this scaler. It
// must be among the request's SignedHeaders and equal BURST_PUBLIC_URL.
const AudienceHeader = "X-Gha-Scaler-Audience"

const (
	maxTokenLen    = 16 << 10
	maxSTSResponse = 64 << 10
	stsBody        = "Action=GetCallerIdentity&Version=2011-06-15"
	stsTimeout     = 10 * time.Second
	// maxTokenAge is how old a signed request may be. STS itself accepts 15 min.
	maxTokenAge = 5 * time.Minute
	maxSkew     = time.Minute
)

// Token is what a VM sends as its bearer token (base64 of this JSON): an STS
// GetCallerIdentity request it signed with its instance-role credentials, not
// sent. The scaler sends it to STS, which says whose credentials signed it.
type Token struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"` // base64
}

// Verifier checks VM identity tokens: presigned sts:GetCallerIdentity requests
// (the pattern of Vault's AWS IAM auth method).
//
// It accepts only a POST of exactly GetCallerIdentity to an STS endpoint of
// the configured region (or the global one), signed over the audience header
// with the configured value, and no older than maxTokenAge. It then sends the
// request to STS and requires the caller to be
// assumed-role/<InstanceRole>/<instance-id>: the session EC2 itself opens for
// an instance profile. Finally the instance must be one of ours (managed-by
// tag, pending or running, same account, our instance profile), and its Name
// tag is the identity.
type Verifier struct {
	Audience        string // the pull URL
	Region          string
	Role            string // role name in the assumed-role ARN
	InstanceProfile string // name or ARN the VM must carry

	ec2 ec2API
	do  func(*http.Request) (*http.Response, error)
	now func() time.Time
}

// NewVerifier returns a verifier for cfg's audience (BURST_PUBLIC_URL), region
// and instance profile. It uses the default credential chain for the EC2
// lookup; the STS call carries the VM's own signature and no scaler credential.
func NewVerifier(ctx context.Context, cfg config.BurstConfig) (*Verifier, error) {
	ac, err := loadSDKConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: stsTimeout,
		// Never follow a redirect: the request may go to the allowlisted host only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return newVerifier(cfg, ec2.NewFromConfig(ac), client.Do), nil
}

func newVerifier(cfg config.BurstConfig, e ec2API, do func(*http.Request) (*http.Response, error)) *Verifier {
	return &Verifier{
		Audience: cfg.PublicURL, Region: cfg.AWS.Region,
		Role: cfg.AWS.InstanceRole, InstanceProfile: cfg.AWS.InstanceProfile,
		ec2: e, do: do, now: time.Now,
	}
}

var errIdentity = errors.New("identity token rejected")

func reject(check string) error { return fmt.Errorf("%w: %s", errIdentity, check) }

var (
	authRE       = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([A-Za-z0-9]+)/([0-9]{8})/([a-z0-9-]+)/([a-z0-9-]+)/aws4_request, ?SignedHeaders=([a-z0-9;-]+), ?Signature=[0-9a-f]{64}$`)
	arnRE        = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):sts::([0-9]{12}):assumed-role/([A-Za-z0-9+=,.@_-]{1,64})/(i-[0-9a-f]{8}(?:[0-9a-f]{9})?)$`)
	instanceIDRE = regexp.MustCompile(`^i-[0-9a-f]{8}(?:[0-9a-f]{9})?$`)
)

// Verify checks the token and returns the VM's Name tag. Errors name the
// failed check only, never the token or any part of it.
func (v *Verifier) Verify(ctx context.Context, token string) (burst.Identity, error) {
	if v.Audience == "" || v.Region == "" || v.Role == "" {
		return burst.Identity{}, reject("verifier not configured")
	}
	req, err := v.request(ctx, token)
	if err != nil {
		return burst.Identity{}, err
	}
	arn, err := v.callerARN(req)
	if err != nil {
		return burst.Identity{}, err
	}
	m := arnRE.FindStringSubmatch(arn)
	switch {
	case m == nil:
		return burst.Identity{}, reject("caller is not an EC2 instance-profile session")
	case m[3] != v.Role:
		return burst.Identity{}, reject("caller role")
	}
	return v.instance(ctx, m[2], m[4])
}

// request decodes and checks the token and builds the request to send to STS.
func (v *Verifier) request(ctx context.Context, token string) (*http.Request, error) {
	if len(token) > maxTokenLen {
		return nil, reject("token too large")
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, reject("token encoding")
	}
	var t Token
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, reject("token format")
	}
	if t.Method != http.MethodPost {
		return nil, reject("method")
	}

	// Where the request goes: an STS endpoint, nothing else (no SSRF).
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "/" && u.Path != "") || u.Opaque != "" {
		return nil, reject("sts url")
	}
	host := u.Hostname()
	if host != "sts.amazonaws.com" && host != "sts."+v.Region+".amazonaws.com" {
		return nil, reject("sts host not allowed")
	}

	// What it asks: GetCallerIdentity and nothing more.
	body, err := base64.StdEncoding.DecodeString(t.Body)
	if err != nil {
		return nil, reject("body encoding")
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || len(form) != 2 || len(form["Action"]) != 1 || len(form["Version"]) != 1 ||
		form.Get("Action") != "GetCallerIdentity" || form.Get("Version") != "2011-06-15" {
		return nil, reject("sts action")
	}

	hdr := http.Header{}
	for k, vals := range t.Headers {
		ck := http.CanonicalHeaderKey(k)
		if len(vals) != 1 || hdr.Get(ck) != "" {
			return nil, reject("headers must have one value each")
		}
		hdr.Set(ck, vals[0])
	}
	am := authRE.FindStringSubmatch(hdr.Get("Authorization"))
	if am == nil {
		return nil, reject("authorization header")
	}
	scopeDate, scopeService := am[2], am[4]
	signed := strings.Split(am[5], ";")
	if scopeService != "sts" {
		return nil, reject("credential scope service")
	}
	for _, h := range []string{"host", "x-amz-date", strings.ToLower(AudienceHeader)} {
		if !slices.Contains(signed, h) {
			return nil, reject(h + " not signed")
		}
	}

	// For whom: our audience, inside the signature.
	if hdr.Get(AudienceHeader) != v.Audience {
		return nil, reject("audience")
	}
	// When: fresh.
	ts, err := time.Parse("20060102T150405Z", hdr.Get("X-Amz-Date"))
	if err != nil || ts.Format("20060102") != scopeDate {
		return nil, reject("x-amz-date")
	}
	now := v.now()
	if ts.Before(now.Add(-maxTokenAge)) || ts.After(now.Add(maxSkew)) {
		return nil, reject("stale or future-dated")
	}
	if h := hdr.Get("Host"); h != "" && !strings.EqualFold(h, u.Host) {
		return nil, reject("host header")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", bytes.NewReader(body))
	if err != nil {
		return nil, reject("request")
	}
	req.Host = host
	// Forward the signed headers and the signature only. Host and
	// Content-Length are set by the HTTP client from the URL and the body.
	for _, h := range signed {
		ck := http.CanonicalHeaderKey(h)
		val := hdr.Get(ck)
		switch {
		case h == "host":
			continue
		case h == "content-length":
			if val != "" && val != fmt.Sprint(len(body)) {
				return nil, reject("content-length")
			}
			continue
		case !headerPresent(hdr, ck):
			return nil, reject("a signed header is missing")
		}
		req.Header.Set(ck, val)
	}
	req.Header.Set("Authorization", hdr.Get("Authorization"))
	return req, nil
}

func headerPresent(h http.Header, k string) bool { _, ok := h[k]; return ok }

// callerARN sends the request to STS and returns the caller's ARN.
func (v *Verifier) callerARN(req *http.Request) (string, error) {
	ctx, cancel := context.WithTimeout(req.Context(), stsTimeout)
	defer cancel()
	resp, err := v.do(req.WithContext(ctx))
	if err != nil {
		// The client error may quote the URL; it holds no secret, but keep
		// the reason generic anyway.
		return "", reject("sts unreachable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSTSResponse))
	if err != nil {
		return "", reject("sts response read")
	}
	if resp.StatusCode != http.StatusOK {
		return "", reject(fmt.Sprintf("sts status %d", resp.StatusCode))
	}
	arn, err := parseCallerIdentity(resp.Header.Get("Content-Type"), data)
	if err != nil || arn == "" {
		return "", reject("sts response format")
	}
	return arn, nil
}

// parseCallerIdentity reads the Arn out of a GetCallerIdentity response, in
// the query protocol's XML (the default) or its JSON form.
func parseCallerIdentity(contentType string, data []byte) (string, error) {
	mt, _, _ := mime.ParseMediaType(contentType)
	if strings.HasSuffix(mt, "json") {
		var r struct {
			GetCallerIdentityResponse struct {
				GetCallerIdentityResult struct{ Arn string }
			}
			Arn string // the JSON protocol's flat shape
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", err
		}
		if a := r.GetCallerIdentityResponse.GetCallerIdentityResult.Arn; a != "" {
			return a, nil
		}
		return r.Arn, nil
	}
	var r struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Result  struct {
			Arn string `xml:"Arn"`
		} `xml:"GetCallerIdentityResult"`
	}
	if err := xml.Unmarshal(data, &r); err != nil {
		return "", err
	}
	return r.Result.Arn, nil
}

// instance looks the caller's instance up and returns its Name tag.
func (v *Verifier) instance(ctx context.Context, account, id string) (burst.Identity, error) {
	if !instanceIDRE.MatchString(id) {
		return burst.Identity{}, reject("instance id")
	}
	out, err := v.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return burst.Identity{}, reject("instance lookup failed")
	}
	if len(out.Reservations) != 1 || len(out.Reservations[0].Instances) != 1 {
		return burst.Identity{}, reject("instance not found")
	}
	r := out.Reservations[0]
	in := r.Instances[0]
	switch {
	case awssdk.ToString(in.InstanceId) != id:
		return burst.Identity{}, reject("instance id mismatch")
	case awssdk.ToString(r.OwnerId) != account:
		return burst.Identity{}, reject("account")
	case tagValueOf(in.Tags, tagManagedBy) != tagValue:
		return burst.Identity{}, reject("instance not managed by this scaler")
	case stateOf(in) != ec2types.InstanceStateNamePending && stateOf(in) != ec2types.InstanceStateNameRunning:
		return burst.Identity{}, reject("instance state")
	case in.IamInstanceProfile == nil || !profileMatches(awssdk.ToString(in.IamInstanceProfile.Arn), v.InstanceProfile):
		return burst.Identity{}, reject("instance profile")
	}
	name := tagValueOf(in.Tags, tagName)
	if name == "" {
		return burst.Identity{}, reject("instance has no Name tag")
	}
	return burst.Identity{InstanceName: name}, nil
}

// profileMatches compares an instance's profile ARN with the configured
// profile name or ARN.
func profileMatches(arn, want string) bool {
	if arn == "" || want == "" {
		return false
	}
	if strings.HasPrefix(want, "arn:") {
		return arn == want
	}
	_, rest, ok := strings.Cut(arn, ":instance-profile/")
	if !ok {
		return false
	}
	return rest[strings.LastIndex(rest, "/")+1:] == want
}
