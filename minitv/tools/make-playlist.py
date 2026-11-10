#!/usr/bin/env python3
"""Write an M3U playlist with the live streams of the German-language public broadcasters.

Usage: python3 make-playlist.py [--no-guide] [out.m3u]        (default: playlist.m3u)

--no-guide writes guide="off" in the header and no zapp-id: the TV app then has no channel list and no programme
texts and never asks the guide server.

The URLs come from the public MediathekViewWeb API, the way Axel-Erfurt describes in
https://gist.github.com/Axel-Erfurt/b40584d152e1c2f13259590a135e05f4 (comment 6036351).
Only the permanent channels are kept: entries with the topic "Livestream" and an HLS URL.
Event streams and old clips also match the search and are dropped that way.
"""
import json
import sys
import urllib.request

API = "https://mediathekviewweb.de/api/query"
# The order of the playlist, so the order of the channel keys.
CHANNELS = ["ard", "zdf", "mdr", "phoenix", "rbb", "br", "hr", "sr", "swr", "ndr",
            "dw", "wdr", "arte", "3sat", "kika", "orf", "srf"]
# Other things people say for a channel; the voice button of the TV app understands them. Written into the
# playlist as aliases="a;b" in the #EXTINF line. Add your own here or edit the playlist by hand.
ALIASES = {
    "ARD": "Das Erste;Erstes;Das Erste Deutsches Fernsehen",
    "ARD ONE": "One",
    "ARD Alpha": "Alpha;BR Alpha",
    "ARD Tagesschau": "Tagesschau;Tagesschau 24",
    "ZDF": "Zweites;Zweites Deutsches Fernsehen",
    "MDR Sachsen": "Sachsen;Mitteldeutscher Rundfunk Sachsen",
    "MDR Thüringen": "Thüringen;Mitteldeutscher Rundfunk Thüringen",
    "MDR Sachsen-Anhalt": "Sachsen-Anhalt;Mitteldeutscher Rundfunk Sachsen-Anhalt",
    "PHOENIX": "Phönix",
    "RBB Berlin": "Berlin;Rundfunk Berlin-Brandenburg Berlin",
    "RBB Brandenburg": "Brandenburg;Rundfunk Berlin-Brandenburg Brandenburg",
    "BR Nord": "Bayern Nord;Bayerischer Rundfunk Nord;Franken",
    "BR Süd": "Bayern Süd;Bayern;Bayerischer Rundfunk Süd",
    "HR": "Hessen;Hessischer Rundfunk;HR Fernsehen",
    "SR": "Saarland;Saarländischer Rundfunk;SR Fernsehen",
    "SWR BW": "Baden-Württemberg;SWR Baden-Württemberg;Südwestrundfunk Baden-Württemberg",
    "SWR RP": "Rheinland-Pfalz;SWR Rheinland-Pfalz;Südwestrundfunk Rheinland-Pfalz",
    "NDR Hamburg": "Norddeutscher Rundfunk Hamburg",
    "NDR Niedersachsen": "Niedersachsen;Norddeutscher Rundfunk Niedersachsen",
    "NDR Mecklenburg-Vorpommern": "Mecklenburg-Vorpommern;Norddeutscher Rundfunk Mecklenburg-Vorpommern",
    "NDR Schleswig-Holstein": "Schleswig-Holstein;Norddeutscher Rundfunk Schleswig-Holstein",
    "DW": "Deutsche Welle",
    "WDR (Deutschland)": "Westdeutscher Rundfunk;Nordrhein-Westfalen",
    "ARTE DE": "Arte Deutsch;Arte Deutschland",
    "ARTE FR": "Arte Französisch;Arte Frankreich;Arte France",
    "3Sat": "Drei Sat;Dreisat",
    "KiKA": "Kinderkanal;Ki Ka",
    "ORF-1": "ORF Eins;Österreich Eins",
    "ORF-2": "ORF Zwei;Österreich Zwei",
    "ORF-3": "ORF Drei",
}
# The id of a channel at the Zapp backend, which the optional programme guide of the TV app asks (zapp-id in the
# #EXTINF line). Channels not listed (ORF, DW, WDR Lokalzeit, ARTE FR) have no guide.
ZAPP_IDS = {
    "ARD": "das_erste", "ARD ONE": "one", "ARD Alpha": "ard_alpha", "ARD Tagesschau": "tagesschau24",
    "ZDF": "zdf", "ZDF neo": "zdf_neo", "ZDF info": "zdf_info", "PHOENIX": "phoenix", "3Sat": "dreisat",
    "KiKA": "kika", "ARTE DE": "arte", "HR": "hr", "SR": "sr", "WDR (Deutschland)": "wdr",
    "MDR Sachsen": "mdr_sachsen", "MDR Thüringen": "mdr_thueringen", "MDR Sachsen-Anhalt": "mdr_sachsen_anhalt",
    "RBB Berlin": "rbb_berlin", "RBB Brandenburg": "rbb_brandenburg", "BR Nord": "br_nord", "BR Süd": "br_sued",
    "SWR BW": "swr_bw", "SWR RP": "swr_rp", "NDR Hamburg": "ndr_hh", "NDR Niedersachsen": "ndr_nds",
    "NDR Mecklenburg-Vorpommern": "ndr_mv", "NDR Schleswig-Holstein": "ndr_sh",
}
# Regional variants that are hidden (hidden="yes" in the playlist: not zapped to, not listed, not speakable): one
# channel per broadcaster stays, except SWR where both stay. Delete a name here to get the channel back.
# How the TV app says a channel when it announces it (spoken="..." in the #EXTINF line): German letter names, so that
# the multilingual voices do not read "ARD" in English. "WDR Lokalzeit <place>" is made by spoken() below.
SPOKEN = {
    "ARD": "A Er De",
    "ARD ONE": "A Er De Wan",
    "ARD Alpha": "A Er De Alpha",
    "ARD Tagesschau": "A Er De Tagesschau",
    "ZDF": "Zet De Eff",
    "ZDF neo": "Zet De Eff Neo",
    "ZDF info": "Zet De Eff Info",
    "MDR Sachsen": "Em De Er Sachsen",
    "MDR Thüringen": "Em De Er Thüringen",
    "MDR Sachsen-Anhalt": "Em De Er Sachsen-Anhalt",
    "PHOENIX": "Phönix",
    "RBB Berlin": "Er Be Be Berlin",
    "RBB Brandenburg": "Er Be Be Brandenburg",
    "BR Süd": "Be Er Süd",
    "BR Nord": "Be Er Nord",
    "HR": "Ha Er",
    "SR": "Es Er",
    "SWR BW": "Es We Er Be We",
    "SWR RP": "Es We Er Er Pe",
    "NDR Hamburg": "En De Er Hamburg",
    "NDR Niedersachsen": "En De Er Niedersachsen",
    "NDR Schleswig-Holstein": "En De Er Schleswig-Holstein",
    "NDR Mecklenburg-Vorpommern": "En De Er Mecklenburg-Vorpommern",
    "DW": "De We",
    "WDR (Deutschland)": "We De Er",
    "ARTE DE": "Arte Deutsch",
    "ARTE FR": "Arte Französisch",
    "3Sat": "Drei Sat",
    "KiKA": "Kika",
    "ORF-1": "O Er Eff Eins",
    "ORF-2": "O Er Eff Zwei",
    "ORF-3": "O Er Eff Drei",
    "ORF-Sport": "O Er Eff Sport",
    "HDMI": "Ha De Em I",
}


def spoken(name):
    if name.startswith("WDR Lokalzeit "):
        return "We De Er Lokalzeit " + name[len("WDR Lokalzeit "):]
    return SPOKEN.get(name)


HIDDEN = {
    "MDR Thüringen", "MDR Sachsen-Anhalt", "RBB Brandenburg", "BR Nord", "NDR Hamburg", "NDR Schleswig-Holstein",
    "NDR Mecklenburg-Vorpommern", "WDR Lokalzeit Bonn", "WDR Lokalzeit Essen", "WDR Lokalzeit Aachen",
    "WDR Lokalzeit Siegen", "WDR Lokalzeit Münster", "WDR Lokalzeit Dortmund", "WDR Lokalzeit Duisburg",
    "WDR Lokalzeit Bielefeld", "WDR Lokalzeit Wuppertal", "WDR Lokalzeit Düsseldorf",
    # The ORF streams are blocked outside Austria (403 in Germany): they stay in the file, at the end, for a unit
    # in Austria or behind a VPN. Remove these names to show them.
    "ORF-1", "ORF-2", "ORF-3", "ORF-Sport",
    # Not in German.
    "ARTE FR",
}
# Channel 1, 2, 3 (0 is HDMI).
FIRST = ["ARD", "ZDF", "SWR BW"]
HEADERS = {
    "Content-Type": "text/plain;charset=UTF-8",
    "User-Agent": "Mozilla/5.0 (X11; Linux x86_64; rv:10.0) Gecko/20100101 Firefox/10.0",
}


def livestreams(channel):
    body = {
        "queries": [
            {"fields": ["title", "topic"], "query": "livestream"},
            {"fields": ["channel"], "query": channel},
        ],
        "sortBy": "timestamp", "sortOrder": "desc", "future": True, "offset": 0, "size": 100,
    }
    request = urllib.request.Request(API, json.dumps(body).encode(), HEADERS)
    with urllib.request.urlopen(request, timeout=30) as response:
        results = json.load(response)["result"]["results"]
    found = []
    for entry in results:
        url = entry.get("url_video") or ""
        if entry.get("topic") == "Livestream" and ".m3u8" in url:
            name = entry["title"].replace(".", " ").replace(" Livestream", "").strip()
            found.append((name, url))
    # The plain channel name before its variants (ARD before ARD Alpha).
    return sorted(found, key=lambda f: (len(f[0]), f[0].lower()))


def main():
    args = [a for a in sys.argv[1:] if a != "--no-guide"]
    guide = len(args) == len(sys.argv) - 1
    out = args[0] if args else "playlist.m3u"
    seen = set()
    lines = ["#EXTM3U" if guide else '#EXTM3U guide="off"']
    # Channel 0: the HDMI input, shown inside the TV app (address "hdmi:"); "Eingang" and "Computer" can be said too.
    lines += [f'#EXTINF:-1 aliases="Eingang;Computer" spoken="{spoken("HDMI")}",HDMI', "hdmi:"]
    entries = []
    for channel in CHANNELS:
        try:
            found = livestreams(channel)
        except (OSError, ValueError, KeyError) as error:
            print(f"{channel}: {error}", file=sys.stderr)
            continue
        for name, url in found:
            if url not in seen:
                seen.add(url)
                aliases = ALIASES.get(name)
                zapp = ZAPP_IDS.get(name) if guide else None
                attributes = (' hidden="yes"' if name in HIDDEN else "") + (f' zapp-id="{zapp}"' if zapp else "") + (f' aliases="{aliases}"' if aliases else "")
                said = spoken(name)
                attributes += f' spoken="{said}"' if said else ""
                entries.append((name, f"#EXTINF:-1{attributes},{name}", url))
    # The channel numbers are the positions in the file (HDMI is 0): these come first, in this order, the rest
    # keeps its order.
    entries.sort(key=lambda e: FIRST.index(e[0]) if e[0] in FIRST else len(FIRST))
    for _, extinf, url in entries:
        lines += [extinf, url]
    if len(lines) == 1:
        sys.exit("no streams found, nothing written")
    with open(out, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(f"{len(seen)} channels written to {out}, {sum(1 for l in lines if 'hidden=' in l)} of them hidden")


if __name__ == "__main__":
    main()
