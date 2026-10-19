# shellcheck shell=sh
# Host tools always win. These root-owned generic shims dispatch into the guest;
# neither the Brew prefix nor guest executables are exposed in the host PATH.
case ":${PATH-}:" in
    *:/var/lib/bluefin-homebrew/commands:*) ;;
    *)
        # shellcheck disable=SC2123 # appends to PATH, keeping what is there
        PATH="${PATH:+$PATH:}/var/lib/bluefin-homebrew/commands" ;;
esac
export PATH
