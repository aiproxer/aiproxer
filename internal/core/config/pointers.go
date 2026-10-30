package config

func BoolPtr(v bool) *bool {
	return new(v)
}
