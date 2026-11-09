# FirmwareOps HIL Runner

Open-source hardware-in-the-loop runner for embedded firmware CI. Build firmware with your existing CI, then use a trusted Linux self-hosted GitHub Actions runner connected to a real board to flash it and collect UART output as an artifact.

This repository starts with one intentionally narrow profile: **ESP32-S3 with an 8 MiB flash layout**. It writes the bootloader, partition table, and application together at the ESP-IDF offsets `0x0000`, `0x8000`, and `0x10000`.

## Why a self-hosted runner?

GitHub-hosted runners do not have your USB board attached. Keep ordinary builds and tests on GitHub-hosted runners; route only the hardware job to a dedicated, trusted machine in your lab. The runner must have access to the serial device and Docker for the example build workflow.

Do not run untrusted pull-request code on a persistent self-hosted runner connected to hardware. Use repository and runner-group restrictions, and require approval for workflows that can flash devices.

## Requirements

- Linux x86-64 self-hosted GitHub Actions runner with USB access to the ESP32-S3.
- Go 1.22 or newer, `esptool` on `PATH`, and `stty` for UART capture.
- Firmware artifacts produced for this exact board profile. The partition table must place an app partition at `0x10000`, and all partitions must fit within 8 MiB.
- The board must be in its serial bootloader mode when flashing starts.

Install esptool in the runner environment, for example with a pinned Python environment and `pip install esptool`. Keep the version consistent across your lab.

## GitHub Actions

Copy [the example workflow](examples/esp32s3-hil.yml) into your firmware repository and adjust the firmware binary name, port, and runner labels. Register the lab machine with labels such as `self-hosted`, `linux`, `x64`, `hil`, and `esp32s3`.

The workflow builds on a GitHub-hosted runner, transfers the three flash images as an artifact, then runs the HIL action on the machine with the board attached. The action:

1. Checks the three files and the ESP-IDF partition layout.
2. Uses esptool to identify the flash and refuses devices smaller than 8 MiB or an unknown capacity.
3. Writes and verifies bootloader, partition table, and application in one operation.
4. Optionally captures UART output into a log file for upload as a workflow artifact.

The action always writes hardware. Keep it in an explicitly approved hardware job, and pin the action to a reviewed release or commit SHA in production workflows.

## Local use

Build the command and inspect connected devices:

```sh
go build -o hil-runner ./cmd/hil-runner
./hil-runner devices
```

Review the board, port, and artifacts before explicitly confirming a flash:

```sh
./hil-runner flash \
  --family esp32s3 \
  --port /dev/ttyACM0 \
  --bootloader build/bootloader/bootloader.bin \
  --partition-table build/partition_table/partition-table.bin \
  --firmware build/firmwareops_telemetry_gateway.bin \
  --confirm-write
```

Capture serial output separately when needed:

```sh
./hil-runner uart --port /dev/ttyACM0 --baud 115200 --duration 15s --output uart.log
```

The serial device normally needs to be accessible by the runner's OS user (for example, via the `dialout` group). The UART capture starts after flashing, so it records runtime output after the board resets, not the bootloader transfer log.

## Scope and roadmap

This is a focused HIL companion to existing CI, not a replacement for GitHub Actions, firmware build systems, or artifact storage. The first release supports the ESP32-S3 profile above. Other ESP32 layouts and STM32 probe/DFU workflows need explicit board profiles and hardware validation before being advertised as supported.

The runner does not provision Wi-Fi, register devices with a cloud service, or transmit telemetry. Those are application and product-specific concerns.

## License

MIT. See [LICENSE](LICENSE).
