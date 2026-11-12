#!/system/bin/sh
# Hands-free voice control: Amazon's wake word engine starts minitv's voice command.
# The processed mic stream keeps Amazon's echo cancellation and beamforming.
#
# The word and language come from the model path. The first line of
# /system/etc/echowake.conf is the manifest.

MANIFEST=$(cat /system/etc/echowake.conf)
AMP="Digital Volume A"
QUIET_S=7
BEEP=/data/local/tmp/echowake.beep
CAPTURE=/data/media/0/wake.pcm
QUIT_REQUEST=/data/media/0/wake.quit
VOICE_APPS=/data/media/0/voice-apps
FULL=$(tinymix "$AMP" | sed -E 's/.*: ([0-9]+).*/\1/')
# A restart while the amp was muted for a command would otherwise make silence the level to restore.
[ "$FULL" -gt 0 ] 2>/dev/null || FULL=127

# Written by the video call app while a call runs. Only one capture
# slot is free next to Amazon's two recordings, so the call gets it.
IN_CALL=/data/media/0/minicall/in-call
in_call() { [ -e "$IN_CALL" ] && pidof org.minicall >/dev/null; }
while in_call; do sleep 1; done

rm -f "$BEEP" "$QUIT_REQUEST"; mknod "$BEEP" p

# When a call starts, end MicPipe and pryonwake (it does not exit at the end of
# its input): this script ends with the amp restored, and init starts it again
# into the wait above.
(
    while ! in_call; do sleep 1; done
    log -t echowake "video call: giving up the microphone"
    for p in /proc/[0-9]*; do
        case "$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null)" in
        "app_process /system/bin MicPipe "*) kill "${p#/proc/}" ;;
        esac
    done
    kill $(pidof pryonwake) 2>/dev/null
) &

# audioserver refuses recording clients from the adbd domain this service runs in.
CLASSPATH=/system/framework/micpipe.jar runcon u:r:su:s0 app_process /system/bin MicPipe "$BEEP" \
    | /system/bin/pryonwake "$MANIFEST" "$CAPTURE" 7 \
    | while read -r word kind delay; do
        case "$kind" in
        heard)
            echo beep > "$BEEP"
            sleep 0.25
            # At the amp: the media volume takes a second, and the mics hear the speaker.
            tinymix "$AMP" 0
            # Until minitv has finished with the capture and removed it, QUIET_S at most.
            kill "$UNMUTE" 2>/dev/null
            (
                left=$((QUIET_S * 5))
                while [ "$left" -gt 0 ] && [ -e "$CAPTURE" ]; do
                    sleep 0.2
                    left=$((left - 1))
                done
                tinymix "$AMP" "$FULL"
                if [ -e "$QUIT_REQUEST" ]; then
                    rm -f "$QUIT_REQUEST"
                    # Going home would leave a radio playing in the background.
                    cut -d'|' -f2 "$VOICE_APPS" | sort -u | while read -r app; do
                        am force-stop "$app"
                    done
                fi
            ) &
            UNMUTE=$!
            log -t echowake "heard $word, reported $delay samples after its start"
            ;;
        accept)
            # Shared storage only shows the app files owned by media_rw.
            chown 1023:1023 "$CAPTURE"
            am start -a android.speech.action.WEB_SEARCH -n org.minitv/.VoiceActivity --es pcm /sdcard/wake.pcm >/dev/null 2>&1
            ;;
        esac
    done
# A failure must not leave the speaker silent; init then restarts this service.
tinymix "$AMP" "$FULL"
