// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/microsoft/go-infra/stringutil"
	"go.yaml.in/yaml/v4"
	"golang.org/x/text/transform"
)

// cloneNode makes a copy of a YAML node as deep as we need it: a shallow copy
// with a shallow copied slice of Contents.
func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = slices.Clone(n.Content)
	return &c
}

// readYAMLFileDocs reads all YAML documents from the file at path.
func readYAMLFileDocs(path string) ([]*yaml.Node, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readYAMLFileDocsFromReader(f)
}

func readYAMLFileDocsFromReader(r io.Reader) ([]*yaml.Node, error) {
	// YAML parser is fragile with CRLF vs. LF. Normalize to LF before parsing.
	content := transform.NewReader(r, stringutil.CRLFToLF{})
	dec := yaml.NewDecoder(content)

	var nodes []*yaml.Node
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to decode YAML document %d: %w", len(nodes), err)
		}
		nodes = append(nodes, &node)
	}

	return nodes, nil
}

// writeYAMLDoc writes the given YAML Document node to w.
func writeYAMLDoc(w io.Writer, node *yaml.Node) error {
	// If the node is an empty document, this is an encoding error. Just in case
	// this is wanted, turn it into a null scalar. This shows up as an empty
	// file (other than the header comments).
	if node.Kind == yaml.DocumentNode && len(node.Content) == 0 {
		node.Content = []*yaml.Node{{
			Kind:  yaml.ScalarNode,
			Tag:   "!!null",
			Value: "",
		}}
	}

	m := yaml.NewEncoder(w)
	m.SetIndent(2)
	m.DefaultSeqIndent()
	if err := errors.Join(
		m.Encode(node),
		m.Close(),
	); err != nil {
		return fmt.Errorf("failed to encode YAML: %w", err)
	}
	return nil
}

func kindStr(n *yaml.Node) string {
	switch n.Kind {
	case yaml.DocumentNode:
		return "Document"
	case yaml.MappingNode:
		return "Mapping"
	case yaml.SequenceNode:
		return "Sequence"
	case yaml.ScalarNode:
		return "Scalar"
	case yaml.AliasNode:
		return "Alias"
	default:
		return fmt.Sprintf("UnknownKind(%d)", n.Kind)
	}
}

func unwrapIfDocumentNode(n *yaml.Node) (*yaml.Node, error) {
	if n.Kind != yaml.DocumentNode {
		return n, nil
	}
	if len(n.Content) != 1 {
		return nil, fmt.Errorf("document node has %d content nodes (expected 1)", len(n.Content))
	}
	c := n.Content[0]
	// Attempt to preserve header comments.
	appendComments(c, n)
	return c, nil
}

func appendComments(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	dst.HeadComment += src.HeadComment
	dst.LineComment += src.LineComment
	dst.FootComment += src.FootComment
}
