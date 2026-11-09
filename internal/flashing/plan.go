package flashing

import (
	"errors"
	"strconv"
)

type Plan struct {
	Family  string   `json:"family"`
	Tool    string   `json:"tool"`
	Command []string `json:"command"`
}

func BuildESP32S3Plan(port, bootloader, partition, firmware string, baud int) (Plan, error) {
	if port == "" || bootloader == "" || partition == "" || firmware == "" {
		return Plan{}, errors.New("port, bootloader, partition table, and firmware are required")
	}
	if baud <= 0 {
		baud = 460800
	}
	return Plan{
		Family: "esp32s3",
		Tool:   "esptool",
		Command: []string{
			"esptool", "--chip", "esp32s3", "--port", port, "--baud", strconv.Itoa(baud),
			"write-flash", "--flash-mode", "dio", "--flash-size", "8MB", "--flash-freq", "80m",
			"0x0", bootloader, "0x8000", partition, "0x10000", firmware,
		},
	}, nil
}
