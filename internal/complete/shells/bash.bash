# bash completion for vaultr (vaultr completion bash)
# Works with bash 3.2 (macOS) and later.
_vaultr() {
    local line
    local -a cands=()
    while IFS= read -r line; do
        [[ -n $line ]] && cands+=("$line")
    done < <(vaultr __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
    COMPREPLY=()
    for line in "${cands[@]}"; do
        COMPREPLY+=("$(printf '%q' "$line")")
    done
    # A single folder candidate: no space, so tab keeps descending.
    if [[ ${#cands[@]} -eq 1 && ${cands[0]} == */ ]]; then
        compopt -o nospace 2>/dev/null
    fi
    return 0
}
complete -F _vaultr vaultr
