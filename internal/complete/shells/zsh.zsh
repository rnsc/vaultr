#compdef vaultr
# zsh completion for vaultr (vaultr completion zsh)
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
if (( ! $+functions[compdef] )); then
    autoload -Uz compinit && compinit -i
fi
compdef _vaultr vaultr
