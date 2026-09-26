package unit

// systemdTemplate is the text/template source for the systemd user unit.
// %h expands to the home directory in systemd unit files.
const systemdTemplate = `[Unit]
Description=Nexus Controller (Slack agent front door)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.NexusBinDir}}/nexus controller serve --config {{.ConfigPath}}

Environment=PATH={{.NexusBinDir}}:%h/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=TMPDIR=/var/tmp

LoadCredentialEncrypted=` + CredentialName + `

Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
`

const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.nexus.controller</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.NexusBinDir}}/nexus</string>
        <string>controller</string>
        <string>serve</string>
        <string>--config</string>
        <string>{{.ConfigPath}}</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.NexusBinDir}}:/usr/local/bin:/usr/bin:/bin</string>
        <key>TMPDIR</key>
        <string>/var/tmp</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>/tmp/com.nexus.controller.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/com.nexus.controller.log</string>
</dict>
</plist>
`
