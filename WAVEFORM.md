# Radio Bandwidth as a FlexRadio waveform

FLEX-8000 series and Aurora radios (SmartSDR 4.2.18 and later) can run Docker containers as
waveforms. Installed this way, Radio Bandwidth runs inside the radio and answers tests by itself, so
operators can test the network path between their PC and the radio, which is what SmartSDR
audio, panadapter and DAX actually use.

It has no screen and no settings. It:

- answers tests from any computer running Radio Bandwidth (TCP and UDP port 47830);
- announces itself on the LAN every 2 seconds (UDP 47831), so it appears in every Radio Bandwidth's
  list with a **Radio** tag;
- reads the radio's model, nickname and callsign from the radio's own API (read only, the
  `info` command on port 4992), and announces itself as, for example,
  **FLEX-8600 Shack (VA3MW)**. Until the radio API answers (it starts after the container) it
  uses the container's host name, then switches.

**Status: first version, not yet tried on a radio.** It has been tested in Docker on a PC
against a simulated radio API. The checklist below is the first test on real hardware.

## Install

1. Get `radio-bandwidth-waveform-<version>.tar.gz` (built for arm64, the radio's CPU).
2. In SmartSDR, open the **Waveform Manager**, click **Install** and choose the file.
3. The radio starts the container. It also starts automatically after every reboot.

To remove it, uninstall it in the Waveform Manager.

## Test it

On a PC on the same LAN as the radio, run Radio Bandwidth. Within a few seconds the radio appears in
**Pick the other computer** with a Radio tag. Click **Test**.

If it does not appear, type the radio's IP address (for example `192.168.110.78`) and click
**Start test**.

### First test on a radio

Please check and report:

1. The radio appears in the list, and under which name (model, nickname, callsign).
2. A full test completes, with results in every row (speed, delay, jitter, UDP stream, MTU).
3. Whether a test disturbs the radio while it is in use: audio, panadapter, CW timing. Run one
   test while listening to a busy band, and one while transmitting into a dummy load.
4. If it does, try the speed-limited build (below) and repeat.
5. After a radio reboot, the radio appears again without reinstalling.

## Speed limit

A full test pushes up to about 940 Mb/s of TCP and a 100 Mb/s UDP stream through the radio's
CPU, which might disturb live operation. The radio cannot pass options at install time, so a
limit is built into the image instead:

```bash
MAX_MBPS=20 ./build-waveform.sh
```

This makes `radio-bandwidth-waveform-<version>-max20mbps.tar.gz`. Every PC that tests this radio keeps
its traffic under 20 Mb/s in both directions, the UDP stream included. The results show the
speed without grading it and say that the radio limited it. Delay, jitter, loss and MTU are
measured as normal. Whether a limit is needed, and what value, is one of the things the first
test on a radio should answer.

## Tailscale

**LAN only for now.** On these radios Tailscale runs inside its own container
([flex-tailnet-shim](https://github.com/aethersdr/AetherSDR/tree/main/tools/flex-tailnet-shim)) with
its own network stack, so the radio's Tailscale address only reaches ports that container
publishes, and it publishes TCP only. Radio Bandwidth needs UDP for most of its tests (stream loss,
jitter, MTU), so testing the radio over Tailscale would give incomplete and misleading results.
UDP publishing in the shim would make it possible.

## How the image is built

```bash
./build-waveform.sh
```

1. Builds a static Linux binary for arm64 (no C library, nothing else needed).
2. Writes `NOTICES` with the licences of the third-party code linked in.
3. Packs both into an OCI container image with `tools/mkwaveform`: `FROM scratch` (no operating
   system inside), entry point `/radio-bandwidth --responder`, and the two labels the radio reads:
   `com.flexradio.waveform.name=radio-bandwidth` and `com.flexradio.waveform.version=<version>`.

The result is about 2.7 MB. No Docker daemon is needed to build it, and the same source always
gives a byte-for-byte identical file.

To try it with Docker on a PC:

```bash
ARCH=amd64 ./build-waveform.sh
```

```bash
docker load -i dist/radio-bandwidth-waveform-<version>-amd64.tar.gz
```

```bash
docker run --rm --network host -e SSDR_RADIO_ADDRESS=<radio-ip> <image-id>
```

Use `docker images` to find the image ID. `--network host` matches the radio, where containers
share the radio's own network; LAN discovery depends on it.

## What the radio provides

| | |
|---|---|
| CPU | arm64 (Cortex-A72) |
| Network | the container shares the radio's own network |
| `SSDR_RADIO_ADDRESS` | the radio API's address, normally 172.30.1.1. Radio Bandwidth switches to responder mode when it is set |
| Start | automatically, about 20 seconds after the radio boots, before the radio API is up |
| Logs | none; the container's output is not visible |
| Ports in use by the radio | 4991 to 4995, 42607, 48992, 48993. Radio Bandwidth uses 47830 and 47831 |

Most of this was learned from the AetherSDR project's
[flex-tailnet-shim](https://github.com/aethersdr/AetherSDR/tree/main/tools/flex-tailnet-shim)
documentation, measured on a FLEX-8600 with firmware 4.2.20.
