# fish completion for vaultr (vaultr completion fish)
function __vaultr_complete
    set -l tokens (commandline -opc)
    vaultr __complete $tokens[2..-1] (commandline -ct) 2>/dev/null
end
complete -c vaultr -f -a '(__vaultr_complete)'
