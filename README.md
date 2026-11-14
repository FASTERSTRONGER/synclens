# SyncLens

Single-binary Go LAN two-way file diff and synchronization tool with a local web UI.

## Features

- **LAN Peer Pairing**: UDP auto-discovery and 6-digit ephemeral PIN authentication (`HMAC-SHA256`).
- **Two-Stage Diff**: Fast metadata scan (byte-exact size + modification time) with optional manual SHA-256 verification and automatic candidate hints for text/code files.
- **File Manager & Flat Views**: Browse directory trees with folder sizes, per-folder or per-file actions, and batch selection.
- **Safe Execution**: Atomic temporary writes, transient LAN retry, broken junction/symlink handling, Windows Recycle Bin deletion by default, live speed/ETA progress, and per-side disk space checks.

## Build

Requires Go 1.22+ (uses standard library only, zero external dependencies):

```bash
go build -o synclens.exe ./cmd/synclens
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
   Open the local web UI (default `http://127.0.0.1:8765`), enter the 6-digit PIN, select the local and peer folders, and run **Scan & Bandingkan**.

## Testing

```bash
go test -v ./...
```
