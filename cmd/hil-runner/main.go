package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/engleandroalves/firmwareops-hil-runner/internal/devices"
	"github.com/engleandroalves/firmwareops-hil-runner/internal/flashing"
	"github.com/engleandroalves/firmwareops-hil-runner/internal/serial"
)

const s3FlashSize = 8 * 1024 * 1024

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "devices":
		err = listDevices()
	case "flash":
		err = flash(os.Args[2:])
	case "uart":
		err = captureUART(os.Args[2:])
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hil-runner:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `FirmwareOps HIL Runner

Usage:
  hil-runner devices
  hil-runner flash --family esp32s3 --port /dev/ttyACM0 --bootloader bootloader.bin --partition-table partition-table.bin --firmware firmware.bin --confirm-write
  hil-runner uart --port /dev/ttyACM0 --baud 115200 --duration 10s --output uart.log`)
}

func listDevices() error {
	found, err := devices.NewScanner(devices.ScanOptions{}).Scan()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(found)
}

func flash(args []string) error {
	flags := flag.NewFlagSet("flash", flag.ContinueOnError)
	family := flags.String("family", "esp32s3", "supported target family: esp32s3")
	port := flags.String("port", "", "serial device, for example /dev/ttyACM0")
	bootloader := flags.String("bootloader", "", "ESP-IDF bootloader binary")
	partition := flags.String("partition-table", "", "ESP-IDF partition table binary")
	firmware := flags.String("firmware", "", "application binary")
	baud := flags.Int("baud", 460800, "esptool baud rate")
	confirm := flags.Bool("confirm-write", false, "required confirmation that this will replace firmware on the connected board")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("refusing to write hardware: pass --confirm-write after verifying the selected board and artifacts")
	}
	if *family != "esp32s3" {
		return fmt.Errorf("unsupported family %q; this release supports esp32s3", *family)
	}
	if *port == "" {
		return errors.New("--port is required")
	}
	if err := validateS3Artifacts(*bootloader, *partition, *firmware); err != nil {
		return err
	}
	if err := verifyFlashCapacity(*port); err != nil {
		return err
	}

	plan, err := flashing.BuildESP32S3Plan(*port, *bootloader, *partition, *firmware, *baud)
	if err != nil {
		return err
	}
	result, err := flashing.Execute(context.Background(), flashing.ExecuteOptions{
		Plan: plan, Timeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	if result.Output != "" {
		fmt.Print(result.Output)
	}
	if result.TimedOut {
		return errors.New("flash command timed out")
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("esptool exited with code %d", result.ExitCode)
	}
	fmt.Println("Flash completed successfully.")
	return nil
}

func validateS3Artifacts(bootloader, partition, firmware string) error {
	for label, path := range map[string]string{
		"bootloader": bootloader, "partition table": partition, "firmware": firmware,
	} {
		if path == "" {
			return fmt.Errorf("%s artifact is required", label)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s artifact: %w", label, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("%s artifact must be a non-empty regular file", label)
		}
	}
	image, err := os.Open(firmware)
	if err != nil {
		return err
	}
	defer image.Close()
	var magic [1]byte
	if _, err := io.ReadFull(image, magic[:]); err != nil || magic[0] != 0xE9 {
		return errors.New("firmware does not have a valid ESP image header (expected 0xE9)")
	}
	if err := validatePartitionTable(partition, firmware); err != nil {
		return err
	}
	return nil
}

func validatePartitionTable(path, firmware string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 0x1000 {
		return errors.New("partition table exceeds its 0x1000-byte flash slot at 0x8000")
	}
	if len(data) < 32 || len(data)%32 != 0 {
		return errors.New("partition table must contain complete 32-byte ESP-IDF entries")
	}
	foundApp := false
	firmwareInfo, err := os.Stat(firmware)
	if err != nil {
		return err
	}
	for offset := 0; offset+32 <= len(data); offset += 32 {
		entry := data[offset : offset+32]
		if entry[0] == 0xFF && entry[1] == 0xFF {
			break
		}
		if entry[0] == 0xEB && entry[1] == 0xEB {
			break
		}
		if entry[0] != 0xAA || entry[1] != 0x50 {
			return fmt.Errorf("invalid partition table entry at byte offset 0x%x", offset)
		}
		address := binary.LittleEndian.Uint32(entry[4:8])
		size := binary.LittleEndian.Uint32(entry[8:12])
		if uint64(address)+uint64(size) > s3FlashSize {
			return fmt.Errorf("partition at 0x%x exceeds the 8 MiB ESP32-S3 flash profile", address)
		}
		if entry[2] == 0 && address == 0x10000 {
			foundApp = true
			if uint64(firmwareInfo.Size()) > uint64(size) {
				return fmt.Errorf("firmware is %d bytes but the application partition at 0x10000 is only %d bytes", firmwareInfo.Size(), size)
			}
		}
	}
	if !foundApp {
		return errors.New("partition table has no application partition at 0x10000; this profile expects the FirmwareOps S3 layout")
	}
	return nil
}

var flashSizePattern = regexp.MustCompile(`(?i)(?:detected\s+flash\s+size|flash\s+size)\s*:?\s*(\d+)\s*(KB|MB|K|M)`)

func verifyFlashCapacity(port string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "esptool", "--chip", "esp32s3", "--port", port, "flash-id")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("timed out while identifying the ESP32-S3 flash")
	}
	if err != nil {
		return fmt.Errorf("could not identify flash capacity: %w: %s", err, strings.TrimSpace(string(output)))
	}
	match := flashSizePattern.FindStringSubmatch(string(output))
	if len(match) != 3 {
		return fmt.Errorf("could not read flash capacity from esptool output; refusing to flash: %s", strings.TrimSpace(string(output)))
	}
	capacity, _ := parseCapacity(match[1], match[2])
	if capacity < s3FlashSize {
		return fmt.Errorf("detected flash is %d bytes; this ESP32-S3 profile requires at least %d bytes", capacity, s3FlashSize)
	}
	fmt.Printf("Detected flash capacity: %s %s\n", match[1], strings.ToUpper(match[2]))
	return nil
}

func parseCapacity(value, unit string) (uint64, error) {
	var size uint64
	if _, err := fmt.Sscan(value, &size); err != nil {
		return 0, err
	}
	if strings.HasPrefix(strings.ToUpper(unit), "M") {
		return size * 1024 * 1024, nil
	}
	return size * 1024, nil
}

func captureUART(args []string) error {
	flags := flag.NewFlagSet("uart", flag.ContinueOnError)
	port := flags.String("port", "", "serial device, for example /dev/ttyACM0")
	baud := flags.Int("baud", 115200, "UART baud rate")
	duration := flags.Duration("duration", 10*time.Second, "capture duration")
	output := flags.String("output", "uart.log", "capture output file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port == "" {
		return errors.New("--port is required")
	}
	path, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	result, err := serial.Capture(serial.CaptureOptions{
		Device: *port, Output: path, Duration: *duration,
		MaxBytes: 8 * 1024 * 1024, Baud: *baud,
	})
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
	return nil
}
