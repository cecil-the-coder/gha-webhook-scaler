package runtime

import (
	"errors"
	"time"

	"github.com/cecil-the-coder/gha-webhook-scaler/internal/config"
)

// ErrNoPrimaryRuntime is returned when RUNTIME=none is asked to start a runner.
var ErrNoPrimaryRuntime = errors.New("no primary runtime (RUNTIME=none): jobs go to burst runners")

// NoneRuntime runs no local runners. With RUNTIME=none every job is handed
// to burst VMs (internal/burst); this runtime only answers "nothing running".
type NoneRuntime struct{}

func (NoneRuntime) StartRunner(*config.Config, string, string) (*RunnerContainer, error) {
	return nil, ErrNoPrimaryRuntime
}
func (NoneRuntime) StartRunnerWithLabels(*config.Config, string, string, []string) (*RunnerContainer, error) {
	return nil, ErrNoPrimaryRuntime
}
func (NoneRuntime) StartRunnerWithLabelsForOwner(*config.Config, *config.OwnerConfig, string, string, []string) (*RunnerContainer, error) {
	return nil, ErrNoPrimaryRuntime
}
func (NoneRuntime) GetRunningRunners(string) ([]RunnerContainer, error) { return nil, nil }
func (NoneRuntime) StopRunner(string) error                             { return nil }
func (NoneRuntime) StopAllRunners(string) error                         { return nil }
func (NoneRuntime) CleanupOldRunners(string, time.Duration) error       { return nil }
func (NoneRuntime) CleanupCompletedRunners(string) error                { return nil }
func (NoneRuntime) GetContainerLogs(string, int) (string, error)        { return "", nil }
func (NoneRuntime) GetRunnerRepository(string) (string, error)          { return "", nil }
func (NoneRuntime) CleanupImages(*config.Config) error                  { return nil }
func (NoneRuntime) GetRuntimeType() config.RuntimeType                  { return config.RuntimeTypeNone }
func (NoneRuntime) HealthCheck() error                                  { return nil }
