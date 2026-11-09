package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type CaptureOptions struct {
	Device   string
	Output   string
	Duration time.Duration
	MaxBytes int64
	Baud     int
}

type CaptureResult struct {
	Device       string `json:"device"`
	Output       string `json:"output,omitempty"`
	BytesWritten int64  `json:"bytesWritten"`
	Duration     string `json:"duration"`
	TimedOut     bool   `json:"timedOut"`
}

func Capture(options CaptureOptions) (CaptureResult, error) {
	if options.Device == "" {
		return CaptureResult{}, errors.New("device is required")
	}
	if options.Duration <= 0 {
		return CaptureResult{}, errors.New("duration must be greater than zero")
	}
	if options.MaxBytes <= 0 {
		return CaptureResult{}, errors.New("max bytes must be greater than zero")
	}
	if options.Baud > 0 {
		if err := configureTTY(options.Device, options.Baud); err != nil {
			return CaptureResult{}, err
		}
	}

	device, err := openSerialDevice(options.Device)
	if err != nil {
		return CaptureResult{}, err
	}
	defer device.Close()

	var output io.Writer = os.Stdout
	var outputFile *os.File
	if options.Output != "" {
		outputFile, err = os.OpenFile(options.Output, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return CaptureResult{}, err
		}
		defer outputFile.Close()
		output = outputFile
	}

	ctx, cancel := context.WithTimeout(context.Background(), options.Duration)
	defer cancel()

	bytesWritten, timedOut, err := CopyUntil(ctx, output, device, options.MaxBytes)
	return CaptureResult{
		Device:       options.Device,
		Output:       options.Output,
		BytesWritten: bytesWritten,
		Duration:     options.Duration.String(),
		TimedOut:     timedOut,
	}, err
}

func openSerialDevice(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func configureTTY(device string, baud int) error {
	if baud <= 0 || !isDevTTY(device) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "stty", "-F", device, strconv.Itoa(baud), "raw", "-echo", "min", "0", "time", "1")
	if output, err := command.CombinedOutput(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("configure serial device %s: timed out", device)
		}
		return fmt.Errorf("configure serial device %s: %w: %s", device, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isDevTTY(device string) bool {
	return strings.HasPrefix(device, "/dev/tty") || strings.HasPrefix(filepath.Base(device), "tty")
}

func CopyUntil(ctx context.Context, writer io.Writer, reader io.Reader, maxBytes int64) (int64, bool, error) {
	if maxBytes <= 0 {
		return 0, false, errors.New("max bytes must be greater than zero")
	}

	done := make(chan copyResult, 1)
	limited := &io.LimitedReader{R: reader, N: maxBytes}

	go func() {
		written, err := io.Copy(writer, limited)
		if err == nil && limited.N == 0 {
			err = fmt.Errorf("capture stopped after reaching max bytes: %d", maxBytes)
		}
		done <- copyResult{written: written, err: err}
	}()

	select {
	case result := <-done:
		return result.written, false, result.err
	case <-ctx.Done():
		return maxBytes - limited.N, true, nil
	}
}

type copyResult struct {
	written int64
	err     error
}
