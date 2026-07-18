package handler

// maxRequestBodySize is the maximum allowed request body size (100 MB).
const maxRequestBodySize = 100 << 20

func initiatorStr(isAgent bool) string {
	if isAgent {
		return "agent"
	}
	return "user"
}
