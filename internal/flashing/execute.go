package flashing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

var allowedTools = map[string]bool{
	"esptool":              true,
	"esptool.py":           true,
	"STM32_Programmer_CLI": true,
	"dfu-util":             true,
	"openocd":              true,
}

type ExecuteOptions struct {
	Plan             Plan
	Timeout          time.Duration
	ProgressInterval time.Duration
	OnProgress       func(output string)
}

type ExecuteResult struct {
	Tool      string    `json:"tool"`
	Command   []string  `json:"command"`
	ExitCode  int       `json:"exitCode"`
	Output    string    `json:"output"`
	TimedOut  bool      `json:"timedOut"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
}

func Execute(ctx context.Context, options ExecuteOptions) (ExecuteResult, error) {
	if err := validateExecutablePlan(options.Plan); err != nil {
		return ExecuteResult{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	startedAt := time.Now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(runCtx, options.Plan.Command[0], options.Plan.Command[1:]...)
	var output synchronizedBuffer
	command.Stdout = &output
	command.Stderr = &output

	var progressDone chan struct{}
	var progressWorkers sync.WaitGroup
	if options.OnProgress != nil {
		interval := options.ProgressInterval
		if interval <= 0 {
			interval = time.Second
		}
		progressDone = make(chan struct{})
		progressWorkers.Add(1)
		go func() {
			defer progressWorkers.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			lastOutput := ""
			for {
				select {
				case <-ticker.C:
					currentOutput := output.String()
					if currentOutput != lastOutput {
						options.OnProgress(currentOutput)
						lastOutput = currentOutput
					}
				case <-progressDone:
					return
				}
			}
		}()
	}

	err := command.Run()
	if progressDone != nil {
		close(progressDone)
		progressWorkers.Wait()
		options.OnProgress(output.String())
	}
	endedAt := time.Now().UTC()
	result := ExecuteResult{
		Tool:      options.Plan.Tool,
		Command:   options.Plan.Command,
		Output:    output.String(),
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}
	if runCtx.Err() != nil {
		result.ExitCode = -1
		result.TimedOut = true
		return result, nil
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
			return result, nil
		}
		return result, err
	}
	result.ExitCode = 0
	return result, nil
}

type synchronizedBuffer struct {
	mu     sync.RWMutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(content []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(content)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return buffer.buffer.String()
}

func validateExecutablePlan(plan Plan) error {
	if plan.Tool == "" {
		return errors.New("flash tool is required")
	}
	if !allowedTools[plan.Tool] {
		return fmt.Errorf("flash tool %q is not allowlisted", plan.Tool)
	}
	if len(plan.Command) == 0 {
		return errors.New("flash command is required")
	}
	if plan.Command[0] != plan.Tool {
		return fmt.Errorf("flash command must start with allowlisted tool %q", plan.Tool)
	}
	return nil
}
