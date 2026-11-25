/*
Copyright 2026 Mohan Raj.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package egress

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ReasonOutOfZone is the reason of a denied span for a host with a declared zone that is not the
// zone of the install.
const ReasonOutOfZone = "EgressOutOfZone"

var errOutOfZone = errors.New("the host is in a different zone")

// wildcard is the start of a host pattern for each name below a domain.
const wildcard = "*."

// HostZones has the zone that the operator declared for egress hosts. A host is a DNS name, or
// "*." and a DNS name for each name below that name. An IP address has no declared zone.
type HostZones struct {
	exact map[string]string
	// below has the zone of each name below the key. The key has no "*." at its start.
	below map[string]string
}

// ParseHostZones reads host=zone pairs with a comma between them. It ignores an empty item. It
// refuses a host or a zone that is not valid, and a host that is in the list 2 times.
func ParseHostZones(list string) (*HostZones, error) {
	z := &HostZones{exact: map[string]string{}, below: map[string]string{}}
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		host, zone, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("the item %q is not host=zone", item)
		}
		if errs := validation.IsDNS1123Label(zone); len(errs) > 0 {
			return nil, fmt.Errorf("the zone of the item %q is not valid: %s", item, strings.Join(errs, "; "))
		}
		into := z.exact
		name := normalHost(host)
		if rest, found := strings.CutPrefix(name, wildcard); found {
			into, name = z.below, rest
		}
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return nil, fmt.Errorf("the host of the item %q is not valid: %s", item, strings.Join(errs, "; "))
		}
		if _, has := into[name]; has {
			return nil, fmt.Errorf("the host of the item %q is in the list 2 times", item)
		}
		into[name] = zone
	}
	return z, nil
}

// Len gives back the count of the hosts with a declared zone.
func (z *HostZones) Len() int {
	if z == nil {
		return 0
	}
	return len(z.exact) + len(z.below)
}

// Zone gives back the declared zone of host. The exact name wins. If there is none, the longest
// pattern wins. Upper case and a dot at the end of the name have no effect.
func (z *HostZones) Zone(host string) (string, bool) {
	if z == nil {
		return "", false
	}
	name := normalHost(host)
	if zone, ok := z.exact[name]; ok {
		return zone, true
	}
	// Each step removes the first label, so the first match is the longest pattern.
	for {
		_, rest, found := strings.Cut(name, ".")
		if !found {
			return "", false
		}
		if zone, ok := z.below[rest]; ok {
			return zone, true
		}
		name = rest
	}
}

func normalHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// outOfZone is true if the host has a declared zone that is not the zone of the install. An
// install with no zone makes no zone check.
func (p *Proxy) outOfZone(host string) bool {
	if p.Zone == "" {
		return false
	}
	zone, ok := p.HostZones.Zone(host)
	return ok && zone != p.Zone
}

// ZoneSink adds the zone of the install to each span.
type ZoneSink struct {
	Sink Sink
	Zone string
}

func (s ZoneSink) Write(sp Span) {
	sp.Zone = s.Zone
	s.Sink.Write(sp)
}
