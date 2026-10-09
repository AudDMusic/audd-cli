package output

// Exit codes. They are part of the CLI's public contract (see README).
const (
	ExitOK         = 0 // success, including "no match"
	ExitUnexpected = 1 // unexpected failure, or no match with --fail-on-no-match
	ExitUsage      = 2 // bad flags, arguments, or missing local tools
	ExitAuth       = 3 // missing, rejected, or expired credentials
	ExitQuota      = 4 // quota, plan, or feature not available
	ExitNetwork    = 5 // network or server error
	ExitSafety     = 6 // a safety bound stopped the command
	ExitPartial    = 7 // batch finished with some failures
	ExitThreshold  = 8 // `usage --check` threshold crossed
)
