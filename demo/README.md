# Demo recording

`demo.gif` in the project README is produced with [VHS](https://github.com/charmbracelet/vhs)
from `demo.tape`. Re-record it whenever the CLI's prompts or output change.

```bash
brew install vhs      # also pulls in ttyd and ffmpeg
vhs demo/demo.tape
```

## Warming the cache first

The tape assumes images are already built and sources already fetched, and that
only `config.json` is missing so the interactive setup runs again. Without that,
the recording sits through a full container build.

```bash
export CORE_SYSTEM_LAUNCHER_HOME=/tmp/vhs-demo

# Build everything once, answering: port 9099, an admin email, option 2.
rm -rf "$CORE_SYSTEM_LAUNCHER_HOME"
printf '9099\ndemo@sdc.nycu.club\n2\n' | ./core-system-launcher up

# Drop the database so the sample forms are imported again on camera, and
# remove the config so the setup questions come back. src/ stays, so the
# sources are not re-fetched and the build runs entirely from cache.
(cd "$CORE_SYSTEM_LAUNCHER_HOME/deploy" && docker compose -p core-system down -v)
rm "$CORE_SYSTEM_LAUNCHER_HOME/config.json"

vhs demo/demo.tape
```

Afterwards, clean up so the demo stack does not linger:

```bash
(cd /tmp/vhs-demo/deploy && docker compose -p core-system down -v)
rm -rf /tmp/vhs-demo
```

## What is elided

The container build is wrapped in VHS `Hide`/`Show`. On a cold machine it takes
minutes and is nothing but compiler output, so leaving it in would bury the part
worth seeing. The project README says as much next to the GIF — the recording
should not imply the whole thing finishes in half a minute.

The `Sleep` inside that hidden block covers what the build costs from a warm
cache. `Hide` only stops frame capture — the terminal keeps scrolling — so
overshooting costs nothing but wall-clock, while undershooting means build
output leaks back into the visible frames.

## Why the OAuth path, not trial mode

Trial mode looks like the easier thing to record, since it needs no Google
Console credentials. It ends by printing a ready-to-paste call to
`/api/auth/login/internal`, which issues a session for any user id without
authenticating. Putting that exact incantation in the first image of the README
hands it to anyone who later wanders past a real deployment, so the tape takes
the OAuth path instead.

That is also the route people actually take, which makes the recording more
useful. The client id and secret typed in are obviously fake and never used:
the recording stops long before anyone signs in.
