# FB2 → EPUB3

Polka uses an external fb2cng `fbc` executable on the first request to
`GET /Images/epub/{id}`. Configure it with `POLKA_FBC_PATH` (default:
`/usr/local/bin/fbc`) or `--fbc-path`. The command is:

```sh
fbc convert --to epub3 --output-file OUTPUT.epub INPUT.fb2
```

Results live in `POLKA_DATA_DIR/epub-cache/<book-id>.epub`. Temporary input
and output live in a private directory beside the cache and are removed after
success, failure, or request cancellation. Only a successful, ZIP-validated
EPUB is atomically renamed into the cache. Invalid cache entries are rebuilt.
Same-book conversions are serialized inside one Polka process; use one server
process per data directory. Cache entries have no automatic expiration: remove
an entry to regenerate it after replacing a source book or converter settings.
The original library is read through Polka's library abstraction.

## NanoPi Docker deployment

Build the image from `DenimTornado/polka`, branch `feature/epub-conversion`.
Keep the existing container's ports, environment, library mount, and persistent
`/data` mount when recreating it. Add these options to its existing `docker run`:

```sh
-e POLKA_FBC_PATH=/usr/local/bin/fbc \
-v /mnt/library/appdata/inpx-web-arm64/bin/fbc:/usr/local/bin/fbc:ro
```

The existing static ARM64 executable must be executable by the container's
`polka` user. No download or bundled converter is needed. The persistent `/data`
mount also persists `/data/epub-cache` and must be writable by `polka`.

For Compose, add to the existing service:

```yaml
environment:
  POLKA_FBC_PATH: /usr/local/bin/fbc
volumes:
  - /mnt/library/appdata/inpx-web-arm64/bin/fbc:/usr/local/bin/fbc:ro
```

Append the mount to the existing volumes; retain the existing `/data` and book
mounts. Deployment and container recreation are a separate manual step after
reviewing the commit.
