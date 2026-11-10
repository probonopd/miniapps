# minicaptive

Keeps a device online in public WLANs (hotels, hospitals, trains) without anyone touching it: it notices the
captive portal, accepts the terms, presses connect, and starts over when that stops working.

## How it works

A foreground service runs a loop (modeled on OpenWrt's travelmate):

1. **Check** over the WLAN (never over mobile data) with the probes operating systems use:
   `connectivitycheck.gstatic.com/generate_204` (204 = online) and `captive.apple.com/hotspot-detect.html`.
   Again every `interval` seconds, and at once whenever a WLAN connects.
   Online means every probe that answered passed (some portals let Google's probe through and nothing else).
   No answer at all on an open WLAN: the gateway is asked too, since some portals hide even DNS until accepted.
2. **Portal** (a probe was intercepted), in this order:
   - **Known API portals** without a form (Deutsche Bahn WIFIonICE / station WLANs: the requests OpenWrt
     travelmate's login scripts send).
   - **WISPr 1.0**: the smart-client XML many hotspot gateways hide in the page; logs in at its `LoginURL`
     (empty or configured credentials), follows proxy and "pending" replies.
   - **Plain HTTP** (fast, no browser): follows redirects, meta refresh, script redirects and frames, keeps
     cookies, picks the form whose button says accept/connect/verbinden/weiter/..., ticks every checkbox, keeps
     hidden fields, fills asked-for fields (email, name, room) only from the settings; or follows such a link.
     Search, payment, voucher, social login and language buttons are avoided.
   - **Hidden WebView** for portals built in JavaScript: the same heuristics as a script in every frame, one
     click per round, a probe after each. Checkboxes, hidden ones included (custom-styled boxes hide the real
     input), and `role=checkbox` elements: native click, then the label, then the property setter that
     framework-controlled inputs listen to, with input/change events.
   - Checkboxes: everything is ticked (terms, AGB, privacy, unknown ones) except advertising consent
     (newsletter, offers, marketing) that the portal does not mark as required. Buttons are scored in German,
     English, French, Spanish, Italian and Dutch; leaving, paying, signing up, social logins, language
     switches and router functions (reset, reboot, firmware) are avoided.
   - A page that defeats both is kept in the app's `files/pages/` (newest 20) for writing a rule.
3. **Which WLAN** (the dropdown, `ssid=`):
   - **A chosen WLAN**: joins it by itself and goes back to it whenever Android switches elsewhere. An open one
     is added automatically; one with a password must be saved once by hand. When its portal cannot be passed
     twice in a row, it starts over (at most every 5 minutes) with a fresh association.
   - **Any open WLAN**: stays on the current WLAN while it gets online. Otherwise it tries the open WLANs in range
     (no password), strongest first, up to 6 per round, each with its portal, until one gets online. A WLAN that
     failed is skipped for 30 minutes (one that worked before and then stopped, typically a session time limit,
     first gets one more try as a new device with a new MAC), and if minicaptive added it, it is removed again so Android does not join
     it later. Protected WLANs are never tried (Android may still join saved ones on its own).
4. **New MAC**: only with root (`mac-rotate=on`, the Echo Show's helper, to come), and only for open WLANs,
   never for ones with a password. Android (15 at least) refuses apps the per-connection random MAC
   ("NETWORK_SETTINGS" only); it gives each WLAN its own fixed random MAC.
5. **Traveling**: a check at once when a WLAN connects or is lost, when the screen goes on, and when a
   candidate comes into range (the chosen WLAN, an untried open one); scan results older than two minutes are
   not used. A chosen WLAN out of range leaves Android's own choice alone. In "any" mode a much stronger open
   WLAN (15 dB) is tried when the current signal falls below -78 dBm, and the old one is rejoined if it does not
   work. "No answer" (tunnel, uplink down) is not a refusal: no new MAC, skipped only 5 minutes; refusing WLANs
   are skipped 30 minutes, doubling up to 4 hours. After a pass Android is told the WLAN works, so it stops
   routing over mobile data. A wake lock keeps a round alive in a pocket; battery optimization should be off.
   A WLAN switched off by the user stays off.
6. Retries after 15 s, up to every 3 minutes when nothing works.

The way that got through (API, WISPr, form, browser) is remembered per WLAN and tried first next time; the
others follow when it stops working. Open WLANs matching `skip=` (default `*Homespot*`, `*_FON*`: community
hotspots that need an account) are never tried.
minicaptive never calls disconnect: Android files that as the user turning a WLAN down and then avoids it.

Privacy: requests carry the user agent every current Chrome on Android sends ("Android 10; K", the device's
Chrome version) and the device's language, nothing that tells minicaptive or the device apart. Android's
WebView names the app in an `X-Requested-With` header on every request, and Google cancelled the switch to turn
that off: so the browser stage makes no requests of its own. Page loads are fetched by minicaptive's HTTP client;
POSTs (forms, fetch, XMLHttpRequest) are handed over by a script put into each page, and only to hosts the portal
loaded pages from. Anything that still reaches the WebView's network (service workers, unknown POSTs) is refused.
Cookies, storage and cache are wiped before every browser visit and after every new connection.

Certificate errors are not overridden: a portal that presents a wrong certificate gets nothing.

## Settings

Edited on the app's screen, or `adb push minicaptive.conf /sdcard/` (taken over at the next round, then deleted
from the card). All optional:

```
ssid=Hotel Guest                 # WLAN to stay on (saved once on the device by hand)
bssid=aa:bb:cc:dd:ee:ff          # access point to prefer
interval=60
probe=http://connectivitycheck.gstatic.com/generate_204
user= / password=                # WISPr or login forms that need them
name= / email= / room=           # only if a portal insists
webview=on
mac-rotate=off                   # root helper only (Echo Show)
rule=*.portal.example click Kostenlos surfen
rule=*.portal.example fill roomnumber=101
```

## The screen

- **Keep the WLAN online**: on/off switch (off after install; remembered across reboots). It switches itself off,
  with a message, when the device is on a WLAN with a password (home, office) that was not chosen in the dropdown.
- Status box: green = Internet, yellow = on the way (portal, joining, trying a WLAN), red = stuck, gray = off;
  with the WLAN (open or not), access point, signal, band, IP address and gateway, and what Android thinks.
  Refreshed every 2 s. Below it, how the last portal was passed (http or browser).
- **WLAN to stay on**: saved and nearby WLANs, or "any" (then minicaptive never disconnects or switches the
  WLAN, it only passes portals). Only a chosen WLAN is reconnected to, at most every 5 minutes, and never while
  Android is still associating.
- "Check now", the settings, and the log.

## Log

With the "Log to file" switch on (off after install), everything goes to `/sdcard/minicaptive/minicaptive.log`
(2 MB, then `.1`). Switching it off offers to delete the file. To read it after a trip:
`adb shell cat /sdcard/minicaptive/minicaptive.log`. Each check logs the open WLANs in range with their signal, the WLAN (name, access point, signal,
band, addresses, gateway, DNS, what Android says: validated / captive portal), every probe (status, size, time,
redirect target, Internet or intercepted), each portal step, and WLAN connects, losses and changes. Also
logcat tag `minicaptive`.

A separate switch adds the location every 5 minutes: Android's own fix (GPS/network) when fresh, else BeaconDB
(beacondb.net, no account) from the access points in range, which then learns them (`_nomap` ones are left out).

## Testing without a hotspot

`tests/portal-sim.py [KIND]` is a fake portal on this machine (kinds `form`, `meta` (refresh + frame), `link`,
`js` (button made by script, login by fetch), `fancy` (hidden styled checkbox, ARIA checkbox, a newsletter box
that must stay unticked, div button), `wispr`). Set `probe=http://THIS-PC:8099/generate_204` on the
device; `curl http://THIS-PC:8099/reset?kind=js` switches kind and forgets passed clients. All five pass on a
moto g34 (Android 15). Put the default probes back afterwards: with the simulator gone the device counts as
offline and reconnects its WLAN.

## Build

`sh build.sh` (shared steps in `../common/`). jsoup (MIT) parses the pages.
