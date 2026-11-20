package composer

import "regexp"

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`)

// ValidModel reports whether id can be given to a runtime as a model. Models are typed by hand
// in each role (and the project's overrides): there is no list to check them against, only this
// format.
func ValidModel(id string) bool { return modelPattern.MatchString(id) }

// ModelFormatProblem is the message for an id ValidModel refuses.
const ModelFormatProblem = "模型 ID 只能用字母、数字和 . _ - : / @ [ ]，最多 100 个字符"
