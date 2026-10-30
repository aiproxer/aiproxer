package accessmode

// ValidateServeModeGate enforces the CLI opt-in contract for multi-user serve:
//
//   - access.mode multi_user requires an explicit `--multi-user` flag on serve.
//   - `--multi-user` with a non-multi_user config is inconsistent and must fail rather
//     than silently doing nothing.
//
// multiUserFlag is nil when the operator did not pass `--multi-user`; a non-nil pointer
// reflects the parsed bool value (including an explicit `--multi-user=false`).
//
// mode is the typed, already-normalized access mode produced by [config.Config.EffectiveAccessMode].
// Invalid raw mode strings are rejected at the config parsing layer by [NormalizeMode]
// (returning [ErrUnknownAccessMode]); an empty mode is treated as single_user for parity
// with that layer. The gate trusts the typed mode and therefore does not re-normalize.
func ValidateServeModeGate(mode Mode, multiUserFlag *bool) error {
	flagSet := multiUserFlag != nil && *multiUserFlag
	switch mode {
	case ModeMultiUser:
		if !flagSet {
			return ErrMultiUserFlagRequired
		}
	case ModeSingleUser, "":
		if flagSet {
			return ErrMultiUserFlagInconsistent
		}
	}
	return nil
}
