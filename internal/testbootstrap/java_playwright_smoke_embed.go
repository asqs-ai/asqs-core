package testbootstrap

import _ "embed"

//go:embed testdata/AsqsPlaywrightSmokeE2E.java.template
var javaPlaywrightSmokeClass string // spring-javaformat:apply (tabs); space indents fail validate
