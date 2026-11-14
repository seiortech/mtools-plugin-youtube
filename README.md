# mtools-plugin-youtube

YouTube Downloader sidecar plugin for **mtools** (`com.mtools.youtube`).

## Build & Package

```sh
go build -o dist/bin/tool .
mtools-plugin pack \
  -manifest manifest.json \
  -surfaces surfaces.json \
  -bin dist/bin/tool \
  -key /path/to/key.priv \
  -out dist/com.mtools.youtube.zip
mtools-plugin verify -bundle dist/com.mtools.youtube.zip
```
