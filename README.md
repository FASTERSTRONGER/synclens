# SyncLens

Single-binary Go LAN two-way file diff and synchronization tool with a local web UI.

- **Developer**: OK Muhammad Majid Maulana
- **Runtime Dependencies**: Zero external runtime dependencies (Go 1.22+ standard library)

## Features

- **LAN Peer Pairing**: UDP auto-discovery and 6-digit ephemeral PIN authentication (`HMAC-SHA256`).
- **3-Way Sync Baseline & Cross-Device Replication**: Tracks the last synchronized snapshot in `synclens-state/` beside the executable (never touching synced data folders) with symmetrical `PairKey` replication over LAN, preventing deleted files from being re-copied while catching delete-vs-edit conflicts.
- **Move & Rename Detection**: Uniquely matches deleted and new paths (by byte size and timestamp) and executes instant `os.Rename` operations on the target device without re-transferring data over LAN.
- **Safety Guards**: Automatic hold when one side scans empty (0 files) against an active baseline, Windows Recycle Bin deletion by default, large-delete (`>= 2 GB`) and mass-delete confirmation warnings, atomic temporary writes, transient LAN retries, broken junction/symlink handling, and per-side disk space checks.
- **Two-Stage Diff & SHA-256 Verification**: Fast metadata scan with manual SHA-256 verification and automatic hints for text/code files.
- **File Manager & Flat Views**: Browse directory trees with folder sizes, deletion/move/conflict filters, live speed and ETA progress.

## Build

Requires Go 1.22+:

```bash
# Optional: regenerate PNG/ICO icons and Windows .syso version/icon resources
go run ./tools/genicon
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out cmd/synclens/rsrc --arch amd64 --product-version=2.0.0.0 --file-version=2.0.0.0

# Build Windows executable
go build -ldflags "-s -w -X main.Version=v2.0" -o synclens.exe ./cmd/synclens
```

## Usage

1. **On the Peer device (e.g., Laptop)**:
   ```bash
   synclens.exe --peer
   ```
   Note the IP address and 6-digit PIN printed in the terminal.

2. **On the Dashboard device (e.g., PC)**:
   ```bash
   synclens.exe
   ```
   Open the local web UI (`http://127.0.0.1:9850`), enter the 6-digit PIN, select the local and peer folders, and run **Scan & Bandingkan**.

## Testing

```bash
go test -v ./...
```
