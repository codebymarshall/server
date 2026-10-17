# Host tools always win. These root-owned generic shims dispatch into the guest;
# neither the Brew prefix nor guest executables are exposed in the host PATH.
case ":${PATH-}:" in
    *:/var/lib/bluefin-homebrew/commands:*) ;;
    *) PATH="${PATH:+$PATH:}/var/lib/bluefin-homebrew/commands" ;;
esac
export PATH
