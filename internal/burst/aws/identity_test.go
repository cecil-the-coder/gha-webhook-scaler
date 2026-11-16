package aws

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	testAccount  = "123456789012"
	testInstance = "i-0123456789abcdef0"
	testVMName   = "gha-burst-x64-0a1b2c3d"
	// Fake credentials, in the AWS documentation's example format.
	testAKID   = "AKIAIOSFODNN7EXAMPLE"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	testSess   = "FwoGZXIvYXdzEXAMPLESESSIONTOKEN"
)

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

type tokenOpts struct {
	host        string
	audience    string
	unsignedAud bool // add the audience header after signing
	body        string
	method      string
	at          time.Time
}

func defaultOpts() tokenOpts {
	return tokenOpts{
		host: "sts.us-east-1.amazonaws.com", audience: testCfg().PublicURL,
		body: stsBody, method: http.MethodPost, at: testNow,
	}
}

func bodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func credentials() awssdk.Credentials {
	return awssdk.Credentials{AccessKeyID: testAKID, SecretAccessKey: testSecret, SessionToken: testSess}
}

// makeToken signs a request with the SDK's SigV4 signer, the way a VM would.
func makeToken(t *testing.T, o tokenOpts) string {
	t.Helper()
	u := "https://" + o.host + "/"
	req, _ := http.NewRequest(o.method, u, strings.NewReader(o.body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if !o.unsignedAud {
		req.Header.Set(AudienceHeader, o.audience)
	}
	region := strings.TrimSuffix(strings.TrimPrefix(o.host, "sts."), ".amazonaws.com")
	if o.host == "sts.amazonaws.com" || !strings.HasPrefix(o.host, "sts.") {
		region = "us-east-1"
	}
	if err := v4.NewSigner().SignHTTP(context.Background(), credentials(), req, bodyHash([]byte(o.body)), "sts", region, o.at); err != nil {
		t.Fatal(err)
	}
	if o.unsignedAud {
		req.Header.Set(AudienceHeader, o.audience)
	}
	hdr := map[string][]string{}
	for k, v := range req.Header {
		hdr[k] = v
	}
	hdr["Host"] = []string{o.host}
	raw, _ := json.Marshal(Token{Method: o.method, URL: u, Headers: hdr, Body: base64.StdEncoding.EncodeToString([]byte(o.body))})
	return base64.StdEncoding.EncodeToString(raw)
}

// fakeSTS checks that the forwarded request still carries a valid signature
// (by re-signing it) and answers with arn.
type fakeSTS struct {
	arn    string
	status int
	json   bool
	calls  int
	hosts  []string
}

func (f *fakeSTS) do(req *http.Request) (*http.Response, error) {
	f.calls++
	f.hosts = append(f.hosts, req.URL.Host)
	body, _ := io.ReadAll(req.Body)
	if !validSigV4(req, body) {
		return resp(http.StatusForbidden, "text/xml", "<ErrorResponse><Error><Code>SignatureDoesNotMatch</Code></Error></ErrorResponse>"), nil
	}
	if f.status != 0 {
		return resp(f.status, "text/xml", "<ErrorResponse/>"), nil
	}
	if f.json {
		return resp(200, "application/json", fmt.Sprintf(`{"GetCallerIdentityResponse":{"GetCallerIdentityResult":{"Account":"%s","Arn":"%s","UserId":"AROAEXAMPLE:%s"}}}`, testAccount, f.arn, testInstance)), nil
	}
	return resp(200, "text/xml", fmt.Sprintf(`<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
    <Arn>%s</Arn>
    <UserId>AROAEXAMPLE:%s</UserId>
    <Account>%s</Account>
  </GetCallerIdentityResult>
  <ResponseMetadata><RequestId>00000000-0000-0000-0000-000000000000</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`, f.arn, testInstance, testAccount)), nil
}

// validSigV4 checks req's AWS4-HMAC-SHA256 signature the way STS would: over
// the headers its SignedHeaders lists, as the request goes on the wire (Host
// from req.Host, Content-Length from the body).
func validSigV4(req *http.Request, body []byte) bool {
	auth := req.Header.Get("Authorization")
	m := authRE.FindStringSubmatch(auth)
	if m == nil || m[1] != testAKID {
		return false
	}
	day, region, service, signed := m[2], m[3], m[4], m[5]
	var canonHdrs strings.Builder
	for _, h := range strings.Split(signed, ";") {
		var v string
		switch h {
		case "host":
			v = req.Host
		case "content-length":
			v = fmt.Sprint(len(body))
		default:
			vals, ok := req.Header[http.CanonicalHeaderKey(h)]
			if !ok {
				return false
			}
			v = strings.Join(strings.Fields(strings.Join(vals, ",")), " ")
		}
		canonHdrs.WriteString(h + ":" + v + "\n")
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, req.URL.RawQuery, canonHdrs.String(), signed, bodyHash(body)}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", req.Header.Get("X-Amz-Date"), scope, bodyHash([]byte(canonical))}, "\n")
	mac := func(k []byte, s string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(s)); return h.Sum(nil) }
	k := mac(mac(mac(mac([]byte("AWS4"+testSecret), day), region), service), "aws4_request")
	return strings.HasSuffix(auth, "Signature="+hex.EncodeToString(mac(k, toSign)))
}

func resp(code int, ct, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(body))}
}

func instanceARN(role string) string {
	return fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", testAccount, role, testInstance)
}

type instOpts struct {
	missing   bool
	owner     string
	managed   bool
	state     ec2types.InstanceStateName
	profile   string
	nameTag   string
	lookupErr error
}

func goodInstance() instOpts {
	return instOpts{owner: testAccount, managed: true, state: ec2types.InstanceStateNameRunning,
		profile: "arn:aws:iam::" + testAccount + ":instance-profile/gha-burst-runner", nameTag: testVMName}
}

func ec2With(o instOpts) *fakeEC2 {
	return &fakeEC2{describeFn: func(in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		if o.lookupErr != nil {
			return nil, o.lookupErr
		}
		if o.missing || len(in.InstanceIds) != 1 || in.InstanceIds[0] != testInstance {
			return &ec2.DescribeInstancesOutput{}, nil
		}
		inst := ec2types.Instance{InstanceId: awssdk.String(testInstance), State: &ec2types.InstanceState{Name: o.state}}
		if o.profile != "" {
			inst.IamInstanceProfile = &ec2types.IamInstanceProfile{Arn: awssdk.String(o.profile)}
		}
		if o.managed {
			inst.Tags = append(inst.Tags, ec2types.Tag{Key: awssdk.String("managed-by"), Value: awssdk.String("gha-webhook-scaler")})
		}
		if o.nameTag != "" {
			inst.Tags = append(inst.Tags, ec2types.Tag{Key: awssdk.String("Name"), Value: awssdk.String(o.nameTag)})
		}
		return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{OwnerId: awssdk.String(o.owner), Instances: []ec2types.Instance{inst}}}}, nil
	}}
}

func testVerifier(sts *fakeSTS, e *fakeEC2) *Verifier {
	v := newVerifier(testCfg(), e, sts.do)
	v.now = func() time.Time { return testNow.Add(30 * time.Second) }
	return v
}

func TestVerify_Accepts(t *testing.T) {
	for _, c := range []struct {
		name string
		host string
		json bool
	}{
		{"regional xml", "sts.us-east-1.amazonaws.com", false},
		{"global", "sts.amazonaws.com", false},
		{"json response", "sts.us-east-1.amazonaws.com", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sts := &fakeSTS{arn: instanceARN("gha-burst-runner"), json: c.json}
			o := defaultOpts()
			o.host = c.host
			id, err := testVerifier(sts, ec2With(goodInstance())).Verify(context.Background(), makeToken(t, o))
			if err != nil {
				t.Fatal(err)
			}
			if id.InstanceName != testVMName {
				t.Errorf("name = %q", id.InstanceName)
			}
			if sts.calls != 1 || sts.hosts[0] != c.host {
				t.Errorf("sts calls %d hosts %v", sts.calls, sts.hosts)
			}
		})
	}
}

func TestVerify_Rejects(t *testing.T) {
	good := instanceARN("gha-burst-runner")
	type tc struct {
		name     string
		opts     func(*tokenOpts)
		token    string // overrides opts
		arn      string
		inst     func(*instOpts)
		status   int
		wantSTS  bool // whether STS should have been called
		wantText string
	}
	cases := []tc{
		{name: "wrong host", opts: func(o *tokenOpts) { o.host = "sts.attacker.example.com" }, wantText: "sts host"},
		{name: "metadata host", opts: func(o *tokenOpts) { o.host = "169.254.169.254" }, wantText: "sts host"},
		{name: "other region", opts: func(o *tokenOpts) { o.host = "sts.eu-west-1.amazonaws.com" }, wantText: "sts host"},
		{name: "lookalike host", opts: func(o *tokenOpts) { o.host = "sts.us-east-1.amazonaws.com.example.com" }, wantText: "sts host"},
		{name: "unsigned audience", opts: func(o *tokenOpts) { o.unsignedAud = true }, wantText: "not signed"},
		{name: "wrong audience", opts: func(o *tokenOpts) { o.audience = "https://other.example.com/v1/runner-config" }, wantText: "audience"},
		{name: "stale", opts: func(o *tokenOpts) { o.at = testNow.Add(-6 * time.Minute) }, wantText: "stale"},
		{name: "future", opts: func(o *tokenOpts) { o.at = testNow.Add(5 * time.Minute) }, wantText: "stale"},
		{name: "other action", opts: func(o *tokenOpts) { o.body = "Action=AssumeRole&Version=2011-06-15&RoleArn=x" }, wantText: "sts action"},
		{name: "get method", opts: func(o *tokenOpts) { o.method = http.MethodGet }, wantText: "method"},
		{name: "garbage", token: "not base64!", wantText: "encoding"},
		{name: "not json", token: base64.StdEncoding.EncodeToString([]byte("{")), wantText: "format"},
		{name: "too large", token: strings.Repeat("A", maxTokenLen+4), wantText: "too large"},
		{name: "wrong role", arn: instanceARN("admin"), wantSTS: true, wantText: "caller role"},
		{name: "user not role", arn: "arn:aws:iam::" + testAccount + ":user/alice", wantSTS: true, wantText: "instance-profile session"},
		{name: "session not instance", arn: "arn:aws:sts::" + testAccount + ":assumed-role/gha-burst-runner/alice", wantSTS: true, wantText: "instance-profile session"},
		{name: "sts refuses", status: http.StatusForbidden, wantSTS: true, wantText: "sts status 403"},
		{name: "missing tag", inst: func(o *instOpts) { o.managed = false }, wantSTS: true, wantText: "not managed"},
		{name: "no name tag", inst: func(o *instOpts) { o.nameTag = "" }, wantSTS: true, wantText: "Name tag"},
		{name: "other account", inst: func(o *instOpts) { o.owner = "210987654321" }, wantSTS: true, wantText: "account"},
		{name: "terminated", inst: func(o *instOpts) { o.state = ec2types.InstanceStateNameShuttingDown }, wantSTS: true, wantText: "state"},
		{name: "other profile", inst: func(o *instOpts) { o.profile = "arn:aws:iam::" + testAccount + ":instance-profile/other" }, wantSTS: true, wantText: "instance profile"},
		{name: "unknown instance", inst: func(o *instOpts) { o.missing = true }, wantSTS: true, wantText: "not found"},
		{name: "lookup error", inst: func(o *instOpts) { o.lookupErr = errors.New("throttled") }, wantSTS: true, wantText: "lookup failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := defaultOpts()
			if c.opts != nil {
				c.opts(&o)
			}
			tok := c.token
			if tok == "" {
				tok = makeToken(t, o)
			}
			io := goodInstance()
			if c.inst != nil {
				c.inst(&io)
			}
			arn := c.arn
			if arn == "" {
				arn = good
			}
			sts := &fakeSTS{arn: arn, status: c.status}
			_, err := testVerifier(sts, ec2With(io)).Verify(context.Background(), tok)
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, errIdentity) || !strings.Contains(err.Error(), c.wantText) {
				t.Errorf("err = %v, want %q", err, c.wantText)
			}
			if (sts.calls > 0) != c.wantSTS {
				t.Errorf("sts called %d times", sts.calls)
			}
			for _, secret := range []string{tok, testSess, testAKID, "Signature="} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks token material: %v", err)
				}
			}
		})
	}
}

func TestVerify_TamperedSignatureFailsAtSTS(t *testing.T) {
	o := defaultOpts()
	raw, _ := base64.StdEncoding.DecodeString(makeToken(t, o))
	var tok Token
	_ = json.Unmarshal(raw, &tok)
	tok.Headers[AudienceHeader] = []string{testCfg().PublicURL} // same value: fine
	tok.Headers["X-Amz-Security-Token"] = []string{"other"}     // signed: breaks the signature
	raw, _ = json.Marshal(tok)
	sts := &fakeSTS{arn: instanceARN("gha-burst-runner")}
	_, err := testVerifier(sts, ec2With(goodInstance())).Verify(context.Background(), base64.StdEncoding.EncodeToString(raw))
	if err == nil || !strings.Contains(err.Error(), "sts status 403") {
		t.Errorf("err = %v", err)
	}
}

func TestProfileMatches(t *testing.T) {
	arn := "arn:aws:iam::123456789012:instance-profile/ci/gha-burst-runner"
	for _, c := range []struct {
		want string
		ok   bool
	}{
		{"gha-burst-runner", true},
		{arn, true},
		{"runner", false},
		{"arn:aws:iam::123456789012:instance-profile/gha-burst-runner", false},
		{"", false},
	} {
		if got := profileMatches(arn, c.want); got != c.ok {
			t.Errorf("%q: %v", c.want, got)
		}
	}
}

// TestBootstrapSigner runs the bootstrap's own Python SigV4 signer and checks
// its token against the SDK signer (via fakeSTS) and the verifier.
func TestBootstrapSigner(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	src := BootstrapScript()
	start := strings.Index(src, "SIGN_PY='\n")
	if start < 0 {
		t.Fatal("no SIGN_PY in bootstrap")
	}
	code := src[start+len("SIGN_PY='\n"):]
	code = code[:strings.Index(code, "\n'\n")]
	creds := fmt.Sprintf(`{"Code":"Success","Type":"AWS-HMAC","AccessKeyId":%q,"SecretAccessKey":%q,"Token":%q}`, testAKID, testSecret, testSess)
	cmd := exec.Command(py, "-I", "-c", code, "us-east-1", testCfg().PublicURL, testNow.Format("20060102T150405Z"))
	cmd.Stdin = strings.NewReader(creds)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	sts := &fakeSTS{arn: instanceARN("gha-burst-runner")}
	id, err := testVerifier(sts, ec2With(goodInstance())).Verify(context.Background(), string(out))
	if err != nil {
		t.Fatal(err)
	}
	if id.InstanceName != testVMName || sts.calls != 1 {
		t.Errorf("id=%+v calls=%d", id, sts.calls)
	}
}
