package devices

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Device struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Driver       string `json:"driver,omitempty"`
	VendorID     string `json:"vendorId,omitempty"`
	ProductID    string `json:"productId,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Board        string `json:"board,omitempty"`
}

type ScanOptions struct {
	DevRoot string
	SysRoot string
}

type Scanner struct {
	devRoot string
	sysRoot string
}

func NewScanner(options ScanOptions) Scanner {
	devRoot := options.DevRoot
	if devRoot == "" {
		devRoot = "/dev"
	}

	sysRoot := options.SysRoot
	if sysRoot == "" {
		sysRoot = "/sys"
	}

	return Scanner{devRoot: devRoot, sysRoot: sysRoot}
}

func (scanner Scanner) Scan() ([]Device, error) {
	entries, err := os.ReadDir(scanner.devRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Device{}, nil
		}
		return nil, err
	}

	devices := make([]Device, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !isSerialDeviceName(name) {
			continue
		}

		device := Device{
			ID:   name,
			Path: filepath.Join(scanner.devRoot, name),
			Kind: "serial",
		}

		enrichFromSysfs(scanner.sysRoot, name, &device)
		if isVirtualSystemSerial(device) {
			continue
		}
		device.Board = inferBoard(device)
		devices = append(devices, device)
	}

	devices = append(devices, scanUSBProgrammingDevices(scanner.sysRoot)...)

	sort.Slice(devices, func(i int, j int) bool {
		return devices[i].Path < devices[j].Path
	})

	return devices, nil
}

func scanUSBProgrammingDevices(sysRoot string) []Device {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "bus", "usb", "devices"))
	if err != nil {
		return nil
	}

	var found []Device
	dfuIndex := 1
	for _, entry := range entries {
		root := filepath.Join(sysRoot, "bus", "usb", "devices", entry.Name())
		vendorID := strings.ToLower(readTrimmed(filepath.Join(root, "idVendor")))
		productID := strings.ToLower(readTrimmed(filepath.Join(root, "idProduct")))
		serial := readTrimmed(filepath.Join(root, "serial"))

		if vendorID == "1366" { // SEGGER
			id := "jlink-" + entry.Name()
			if serial != "" {
				id = "jlink-" + serial
			}
			found = append(found, Device{
				ID:           id,
				Path:         "jlink:" + serial,
				Kind:         "debug-probe",
				Driver:       "jlink",
				VendorID:     vendorID,
				ProductID:    productID,
				Manufacturer: readTrimmed(filepath.Join(root, "manufacturer")),
				Product:      readTrimmed(filepath.Join(root, "product")),
				Serial:       serial,
				Board:        "SEGGER J-Link (STM32 debug probe)",
			})
			continue
		}

		if vendorID == "0483" && productID == "df11" { // STMicroelectronics STM32 DFU bootloader
			path := fmt.Sprintf("usb%d", dfuIndex)
			dfuIndex++
			id := "stm32-dfu-" + entry.Name()
			if serial != "" {
				id = "stm32-dfu-" + serial
			}
			found = append(found, Device{
				ID:           id,
				Path:         path,
				Kind:         "bootloader",
				Driver:       "stm32-dfu",
				VendorID:     vendorID,
				ProductID:    productID,
				Manufacturer: readTrimmed(filepath.Join(root, "manufacturer")),
				Product:      readTrimmed(filepath.Join(root, "product")),
				Serial:       serial,
				Board:        "STM32 USB DFU Bootloader",
			})
		}
	}
	return found
}

func isVirtualSystemSerial(device Device) bool {
	return strings.HasPrefix(device.ID, "ttyS") &&
		device.Driver == "serial8250" &&
		device.VendorID == "" &&
		device.ProductID == ""
}

func inferBoard(device Device) string {
	usbID := strings.ToLower(device.VendorID + ":" + device.ProductID)
	product := strings.ToLower(device.Product)

	switch usbID {
	case "10c4:ea60":
		return "ESP32 DevKit V1 (CP210x)"
	case "1a86:7523", "1a86:55d4":
		return "ESP32 DevKit V1 (CH340)"
	}

	if strings.Contains(product, "cp210") || strings.Contains(product, "ch340") {
		return "ESP32 DevKit V1 compatible"
	}

	return ""
}

func isSerialDeviceName(name string) bool {
	prefixes := []string{
		"ttyUSB",
		"ttyACM",
		"ttyAMA",
		"ttyS",
		"rfcomm",
		"cu.",
		"tty.",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

func enrichFromSysfs(sysRoot string, name string, device *Device) {
	ttyDevicePath := filepath.Join(sysRoot, "class", "tty", name, "device")
	resolvedPath, err := filepath.EvalSymlinks(ttyDevicePath)
	if err != nil {
		resolvedPath = ttyDevicePath
	}

	device.Driver = readDriverName(resolvedPath)

	for current := resolvedPath; current != "" && current != filepath.Dir(current); current = filepath.Dir(current) {
		vendorID := readTrimmed(filepath.Join(current, "idVendor"))
		productID := readTrimmed(filepath.Join(current, "idProduct"))
		if vendorID != "" || productID != "" {
			device.VendorID = vendorID
			device.ProductID = productID
			device.Manufacturer = readTrimmed(filepath.Join(current, "manufacturer"))
			device.Product = readTrimmed(filepath.Join(current, "product"))
			device.Serial = readTrimmed(filepath.Join(current, "serial"))
			return
		}
	}
}

func readDriverName(devicePath string) string {
	driverPath := filepath.Join(devicePath, "driver")
	resolvedPath, err := filepath.EvalSymlinks(driverPath)
	if err != nil {
		return ""
	}

	name := filepath.Base(resolvedPath)
	if name == "." || name == string(filepath.Separator) {
		return ""
	}

	return name
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

func (device Device) TableRow() []string {
	return []string{
		device.Path,
		device.Kind,
		device.Driver,
		formatUSBID(device.VendorID, device.ProductID),
		device.Product,
	}
}

func formatUSBID(vendorID string, productID string) string {
	if vendorID == "" && productID == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", vendorID, productID)
}
