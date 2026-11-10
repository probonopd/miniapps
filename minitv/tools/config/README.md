# minitv-config.html

One HTML file for relatives to set up the minitv TV from a phone or a computer: reminders (the app's `schedule`),
pushed messages, settings, channel, volume and video call. It talks to the TV through the MQTT broker the TV uses
(see "Remote control over MQTT" in [../../README.md](../../README.md)); it needs no server and no internet resources
besides the broker.

## Use

1. Copy `minitv-config.html` to the phone or PC and open it in the browser (file:// is fine).
2. Enter the broker address as a WebSocket URL and the broker user and password:
   - EMQX Cloud serverless: `wss://<name>.emqxsl.com:8084/mqtt`
   - HiveMQ Cloud: `wss://<name>.hivemq.cloud:8884/mqtt`

   The TVs are not typed in: the list at the top is filled from the retained `status` topics of the broker
   (`+/status`, `+/+/status`). A TV that connects or goes offline shows up there at once; every 30 s and when the page
   returns to the foreground a short second connection (the scout) reads the status topics from scratch, so a TV that
   is gone from the broker (or a status missed while the page slept) is dropped from the list. Nothing about the TVs is
   remembered in the browser, so there are no phantom entries; the TV used last time is taken again if the broker
   still shows it, else the first online one.
3. "Zugangsdaten auf diesem Gerät merken" keeps the password in the browser's localStorage and connects by itself at
   the next start; without it the password is only kept until the page is closed (address and user are always
   remembered).

The inputs of "Nachricht jetzt senden" (text, presentation choices) are remembered in localStorage as well
(`minitv-config.v1.message`; they are no secrets) and kept after sending; "Leeren" clears the text.

## Automatic sync

There is no "Senden" for the configuration: every change of the reminders and of the settings goes to the TV by
itself. Switches, choices, presets, copy, delete, move and import go at once; text and number fields after 700 ms
without typing, or at once when the field is left or Enter is pressed. Actions (Jetzt testen, Nachricht senden, Nur
vorlesen, Stopp, Anrufen, Auflegen, Sender, Lautstärke) stay buttons and are sent only while the TV is online.

- What is sent: always the whole state, the reminders as the full array to `<base>/set/schedule` and the settings as
  the full object to `<base>/set/config` (the app checks all or nothing), so the last command always carries
  everything. A reminder whose current values are invalid (shown in red) is sent as its last valid version, or left
  out while it never was valid; a wrong setting keeps its last valid value. The others keep syncing.
- Confirmation: a change counts as saved ("gespeichert" with a check, per reminder and per settings group) when the
  TV's retained `<base>/config` shows it, compared normalized (key order, keys left at their default, numbers sent as
  text do not matter). A refusal on `<base>/result` is shown in German in red. Without a confirmation within 15 s the
  page tries again (3 times, 2, 4 and 8 s apart) and then shows "Keine Antwort vom Fernseher". The badge at the top
  sums it up: Synchron, Wird übertragen ..., Wartet auf Fernseher (offline), Bitte korrigieren, Fehler.
- TV offline: changes are accepted and published **retained**; the app runs a retained command once at its next
  connect and clears it on the broker (the empty message it publishes for that is ignored), so changes made while
  the TV is off arrive even if the page is closed by then. A newer publish replaces the retained one, so only the
  latest full state is ever pending. While the TV is **online** the commands are not retained: MQTT 3.1.1 delivers a
  retained message to a client that is already subscribed without the retained flag, so the app would run it but not
  clear it, and it would be run again at the TV's next start, undoing changes made elsewhere meanwhile
  (`RETAIN_WHEN_ONLINE` in the script). If the TV goes offline before it confirmed a change, the change is sent again
  retained.
- Incoming changes: the TV's retained config is the truth. On load the page shows it and sends nothing. A different
  config (or another page's `set/schedule` / `set/config`, which the page also subscribes to) is taken over live
  when the page has no unconfirmed edits of its own; local edits being typed or not yet confirmed win. Cards and
  controls are patched field by field, the focus and cursor stay. The page only publishes for an edit of its user,
  a bounded retry or a reconnect, never because it took over a change, so two open pages do not bounce changes.
- Delete shows "Rückgängig" for 5 s; import (file or text) asks first and then replaces all reminders.
- Reset: delete the reminders (or import an empty list `[]`) and use "Auf Standard" in the settings. A pending
  retained command can be removed from the broker by publishing an empty retained message to
  `<base>/set/schedule` or `<base>/set/config` while the TV is off (while it is on, the app would answer that with an
  error).

The TV card shows since when an offline TV is gone: the `time` of a clean-stop status ("offline seit 23:03 (vor 3
Minuten)"), else the last heartbeat `time` of `<base>/telemetry` ("zuletzt gesehen um ..."), with the reason in German.

When the screen is red (no picture) or yellow (no internet), the TV card also shows the stream error behind it: the
player's code in plain German with a hint (404/403: wrong address or the station blocks this location; timeout:
check the TV's internet), the raw code and message in a monospace line (tap to copy), the attempt, how long ago,
the channel and the shortened address. Source: `last_stream_error` of `<base>/telemetry` and `stream_error` of
`<base>/state` (newer app); for older apps the newest `stream` event on `<base>/errors` for the current address, or
`last_error` of the telemetry when its type is `stream`. It goes away when the state shows a picture again.

## Reminders and the app's schedule

The editor maps its choices onto the app's `from`/`to`/`every` (the app fires at `from` and every `every` minutes while
the time is before `to`; the same time twice means all day):

| Editor | Schedule entry |
|---|---|
| Einmal täglich um 12:00 | `"from":"12:00","to":"12:01","every":1` |
| Alle 1 Stunden von 08:00 bis 20:00 | `"from":"08:00","to":"20:01","every":60` (the editor's "bis" includes that time) |
| Alle 30 Minuten von 21:00 bis 05:00 | `"from":"21:00","to":"05:01","every":30` (crosses midnight) |
| ... den ganzen Tag | `from` == `to` |

`mode`, `show_seconds`, `repeat` and `pause` are written only where they differ from the TV's settings
`message_mode`, `message_seconds`, `speech_repeat` and `speech_pause`, so a later change of those settings applies to
those reminders too. Ids are made from the text (`mittagessen`, `mittagessen-2`) and kept once assigned; unknown keys
of an entry are kept; the deprecated `speak` is read as `mode`. The limits of the app (20 entries, 1 to 500
characters, every 1 to 1440 minutes, show_seconds 0 to 3600, repeat 1 to 10, pause 0 to 60) are checked in the page,
and the TV's answer on `result` is shown (in German where the message is known).

The presets of the gallery are the `PRESETS` array at the top of the script.

Backups ("Als Datei speichern") are JSON: `{"app":"minitv","kind":"schedule","version":1,"schedule":[...]}`; a bare
schedule array is accepted too.

## Limits

- The schedule has no weekdays or dates: every reminder comes every day.
- The preview uses the clock of the phone or PC; the TV uses its own time zone.
- Reminders only come while the TV app is in front (see the app's README).
- The playlist editor, `cmd/play` and `cmd/shell` are not part of the page on purpose.

## Development

`node test-logic.js` runs the unit tests of the schedule logic (summaries, next times, midnight, all day, ids,
defaults, validation, import, what is sent with last-valid substitution, normalized comparison), the offline texts,
the remembered message inputs, the MQTT packet codec (split and merged frames) and the sync state machine with a fake
clock and transport (confirm, retries, offline, reconnect, refusal, adoption, two editors without ping-pong). The page
itself has no build step.
