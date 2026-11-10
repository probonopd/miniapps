#!/system/bin/sh
# The projector's own camera is the last resort: while another USB video camera is attached, the built-in one is
# unbound from the uvcvideo driver, so that Android (and every app, WebView calls included) sees only the other one;
# when that one is gone the built-in camera is bound again. The built-in camera is the device named in BUILTIN
# (vid:pid). Android's external camera provider follows the removal and addition of /dev/video nodes by itself.
BUILTIN=0bda:5803
DRV=/sys/bus/usb/drivers/uvcvideo
while true; do
  other=0
  held=""
  for i in /sys/bus/usb/devices/*:*; do
    read -r class < "$i/bInterfaceClass" 2>/dev/null || continue
    [ "$class" = 0e ] || continue
    dev=${i%:*}
    read -r vid < "$dev/idVendor"
    read -r pid < "$dev/idProduct"
    if [ "$vid:$pid" = "$BUILTIN" ]; then
      held="$held ${i##*/}"
    elif [ -e "$i/driver" ]; then
      other=1
    fi
  done
  for n in $held; do
    if [ "$other" = 1 ] && [ -e "$DRV/$n" ]; then
      echo "$n" > "$DRV/unbind"
      log -t camera-prefer "built-in camera interface $n unbound, another camera is attached"
    elif [ "$other" = 0 ] && [ ! -e "$DRV/$n" ]; then
      echo "$n" > "$DRV/bind"
      log -t camera-prefer "built-in camera interface $n bound, no other camera"
    fi
  done
  sleep 1
done
