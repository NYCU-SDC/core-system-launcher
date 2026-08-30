# Recording for the README, produced with https://github.com/charmbracelet/vhs
#
#   vhs demo/demo.tape
#
# Re-run this whenever the CLI's prompts or output change; the result is
# deterministic, so the GIF stays in step with the code.
#
# The container build is wrapped in Hide/Show. It takes minutes on a cold
# machine and is nothing but compiler output, so leaving it in would bury the
# part worth showing. The README says so next to the GIF.
#
# The Google OAuth path is recorded rather than trial mode, for two reasons:
# it is the route people actually take, and trial mode ends by printing a
# ready-to-paste call to the internal login endpoint. That endpoint issues a
# session for any user id without authenticating, and putting the exact
# incantation in the first image of the README would be handing it to anyone
# who wanders past a real deployment.
#
# The credentials typed below are obviously fake. They are never used: the
# recording stops well before anyone signs in.
#
# Assumes a warm cache: images already built and sources already fetched, with
# only config.json removed so the interactive setup runs again. See demo/README.

Output demo/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1100
Set Height 720
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 45ms

# Keep the demo out of the real working directory.
Hide
Type "export CORE_SYSTEM_LAUNCHER_HOME=/tmp/vhs-demo && clear"
Enter
Show

Sleep 500ms
Type "./core-system-launcher"
Enter
Sleep 3s

# 1/3 — the public port.
Type "9099"
Sleep 700ms
Enter
Sleep 1.5s

# 2/3 — the admin email.
Type "demo@sdc.nycu.club"
Sleep 700ms
Enter
Sleep 1.5s

# 3/3 — how to sign in. 1 is Google OAuth.
Type "1"
Sleep 700ms
Enter

# Time to read the Google Console instructions and the redirect URI it prints.
Sleep 5s
Enter
Sleep 1s

Type "000000000000-example.apps.googleusercontent.com"
Sleep 500ms
Enter
Sleep 1s

Type "GOCSPX-example-not-a-real-secret"
Sleep 500ms
Enter

# Environment checks, then fetching sources and applying the patches.
#
# This window is tight and worth leaving alone. With a warm cache both steps
# finish in about a second and the build starts scrolling immediately after,
# so a longer sleep leaks build output into the visible frames — which is the
# one thing the Hide below exists to prevent.
Sleep 1500ms

# Everything from here to the finished stack is container build output.
# Hide only stops frame capture; the terminal keeps scrolling, so nothing is
# lost from the final frames.
Hide
Sleep 45s
Show

# The result: services up, sample forms imported, next steps.
Sleep 6s
