#!/system/bin/sh
# Keeps the sound on the USB headset and takes the microphone from a USB webcam when both are attached (see
# AudioRoute.java for why the framework cannot do this itself). Started by service.sh of the Magisk module.
# UsbAlsaManager connects its one selected card a moment after the card shows up in /proc/asound/cards, so the state
# is applied several times after every change; the slow periodic run catches anything the framework redoes later.
# AudioRoute changes nothing when it cannot reach the desired state safely, so a failure leaves the stock behavior.
DIR=${0%/*}
apply() {
  CLASSPATH=$DIR/audioroute.dex app_process /system/bin AudioRoute apply >/dev/null 2>&1 ||
    log -t audioroute "apply failed (exit $?)"
}
until [ "$(getprop sys.boot_completed)" = 1 ]; do sleep 2; done
last=""
since=0
while true; do
  cur=$(cat /proc/asound/cards)
  if [ "$cur" != "$last" ]; then
    last=$cur
    since=0
  fi
  case $since in
    2|5|10|20) apply ;;
    *) [ $((since % 60)) = 30 ] && case $cur in *USB-Audio*) apply ;; esac ;;
  esac
  since=$((since + 1))
  sleep 1
done
