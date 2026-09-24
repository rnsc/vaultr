#compdef vaultr
# zsh completion for vaultr: eval "$(vaultr completion zsh)", or save as
# _vaultr in a directory on $fpath.
_vaultr() {
    local -a cands dirs others
    cands=("${(@f)$(vaultr __complete "${(@Q)words[2,CURRENT]}" 2>/dev/null)}")
    local c
    for c in $cands; do
        [[ -z $c ]] && continue
        if [[ $c == */ ]]; then dirs+=("$c"); else others+=("$c"); fi
    done
    (( ${#dirs} )) && compadd -S '' -- $dirs
    (( ${#others} )) && compadd -- $others
    return 0
}
# Autoloaded from $fpath (the file is named _vaultr, e.g. by Homebrew):
# this file is the function body, so complete now. Loaded with
# eval "$(vaultr completion zsh)": register the function.
if [[ $funcstack[1] == _vaultr ]]; then
    _vaultr "$@"
else
    if (( ! $+functions[compdef] )); then
        autoload -Uz compinit && compinit -i
    fi
    compdef _vaultr vaultr
fi
