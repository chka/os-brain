// Package textguard holds the checks every piece of text passes on its way into or out of a
// brain: the secret scan and the refusals it drives, the size limits, the token rules, and
// the rendering that makes stored text safe to print (OneLine, Fenced, SafeOutput). A brain
// is distilled and read back into an agent's context, so a credential stored by accident or
// a line that impersonates the brief's own headings does harm long after it was written;
// these guards sit below every command so none can skip them.
package textguard
