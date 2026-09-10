package tool

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// shellCommand preserves PowerShell source across Windows command-line quoting.
// The compatibility environment belongs only to this child process, not the
// user's profile. Permission checks and task logs still see the original source.
func shellCommand(profile ShellProfile, workDir, command string) (string, []string) {
	shell, flags := shellCmd(profile, workDir)
	base := strings.ToLower(strings.ReplaceAll(shell, `\`, "/"))
	base = base[strings.LastIndex(base, "/")+1:]
	if base != "powershell" && base != "powershell.exe" && base != "pwsh" && base != "pwsh.exe" {
		return shell, append(flags, command)
	}
	// Only replace the command switch; preserve all configured process options.
	if len(flags) == 0 || !strings.EqualFold(flags[len(flags)-1], "-Command") {
		return shell, append(flags, command)
	}
	source := powerShellCompatibility + "\n" + command
	encoded := utf16.Encode([]rune(source))
	bytes := make([]byte, len(encoded)*2)
	for i, unit := range encoded {
		binary.LittleEndian.PutUint16(bytes[i*2:], unit)
	}
	flags[len(flags)-1] = "-EncodedCommand"
	return shell, append(flags, base64.StdEncoding.EncodeToString(bytes))
}

const powerShellCompatibility = `
$OutputEncoding = [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
if (Get-Alias curl -ErrorAction SilentlyContinue) { Remove-Item Alias:curl -Force }
$artexCurl = Get-Command curl.exe -CommandType Application -ErrorAction SilentlyContinue
if ($artexCurl) { Set-Alias curl $artexCurl.Source }
if (-not (Get-Command head -ErrorAction SilentlyContinue)) {
    # Pipeline-only fallback, not a replacement for the full Unix utility.
    function head {
        begin {
            $artexTake = 10
            if ($args.Count -eq 1 -and "$($args[0])" -match '^-(\d+)$') {
                $artexTake = [int]$Matches[1]
            } elseif ($args.Count -eq 2 -and "$($args[0])" -eq '-n' -and "$($args[1])" -match '^\d+$') {
                $artexTake = [int]$args[1]
            } elseif ($args.Count -ne 0) {
                throw 'head compatibility supports pipelines only: head, head -n N, head -N. Use Get-Content for files.'
            }
            $artexSeen = 0
        }
        process {
            if ($artexSeen -lt $artexTake) { $_; $artexSeen++ }
        }
    }
}
`
