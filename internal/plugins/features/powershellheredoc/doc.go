// Package powershellheredoc owns the opt-in powershell-heredoc-steering feature
// (issue #831): a completed call to a configured shell tool whose command uses
// Bash-style "<<" redirection is withheld and the turn is continued once with a
// hidden instruction, so the model can reissue the call in PowerShell syntax.
//
// The feature is strictly opt-in and fail-open. Each mapped tool declares the
// top-level argument that holds its command and whether it runs PowerShell
// ("powershell") or a mixed or unknown shell ("auto", which steers only when the
// command also carries an unquoted Windows path). The proxy never rewrites,
// executes, or drops a command.
package powershellheredoc
