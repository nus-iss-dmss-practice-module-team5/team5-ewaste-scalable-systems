// Package matching implements the C2 persistence boundary. Python owns evaluation;
// this package independently verifies its complete result before committing it.
package matching

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-api/internal/matchingcontract"
)

// Object is the JSON-shaped contract value exchanged with the matching worker.
// The exported name keeps the public Store methods usable by other packages.
type Object = map[string]any

type object = Object

const timestampLayout = "2006-01-02T15:04:05.000000Z"

func decode(raw []byte) (object, error) {
	value, err := matchingcontract.Decode(raw)
	if err != nil {
		return nil, fail("INVALID_CONTRACT")
	}
	return value, nil
}

func canonical(v any) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(err)
	} // only validated JSON values enter here
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
func digest(v any) string      { sum := sha256.Sum256(canonical(v)); return hex.EncodeToString(sum[:]) }
func equal(a, b any) bool      { return bytes.Equal(canonical(a), canonical(b)) }
func obj(v any) object         { m, _ := v.(object); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func str(v any) string         { s, _ := v.(string); return s }
func boolean(v any) bool       { b, _ := v.(bool); return b }
func number(v any) uint64      { n, _ := strconv.ParseUint(fmt.Sprint(v), 10, 64); return n }
func stamp(t time.Time) string { return t.UTC().Format(timestampLayout) }
func instant(v any) (time.Time, error) {
	s := str(v)
	t, err := time.Parse(timestampLayout, s)
	if err != nil || stamp(t) != s || t.Year() < 1000 {
		return time.Time{}, fail("INVALID_CONTRACT")
	}
	return t, nil
}
func validUUID(v any) bool {
	s := str(v)
	u, e := uuid.Parse(s)
	return e == nil && u.Version() == 4 && u.Variant() == uuid.RFC4122 && u.String() == s
}

var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

func cents(v any) (int64, error) {
	s := str(v)
	if !moneyPattern.MatchString(s) {
		return 0, fail("INVALID_CONTRACT")
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(s, ".", ""), 10, 64)
	if err != nil {
		return 0, fail("INVALID_CONTRACT")
	}
	return n, nil
}
func money(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func selected(m object, keys ...string) object {
	r := object{}
	for _, k := range keys {
		r[k] = m[k]
	}
	return r
}

func validate(name string, value object) error {
	// Round-trip internal uints into JSON numbers for the schema implementation.
	normal, err := decode(canonical(value))
	if err != nil {
		return err
	}
	if err := matchingcontract.Validate(name, normal); err != nil {
		return fail("INVALID_CONTRACT")
	}
	return nil
}

func normalize(input object) {
	orgs := arr(input["organisations"])
	sort.Slice(orgs, func(i, j int) bool {
		return str(obj(orgs[i])["recycler_org_id"]) < str(obj(orgs[j])["recycler_org_id"])
	})
	for _, v := range orgs {
		o := obj(v)
		for _, spec := range [][2]string{{"category_capabilities", "category"}, {"capacity_pools", "id"}, {"service_zones", "zone"}} {
			a := arr(o[spec[0]])
			sort.Slice(a, func(i, j int) bool {
				x, y := obj(a[i]), obj(a[j])
				if x[spec[1]] == y[spec[1]] {
					return str(x["id"]) < str(y["id"])
				}
				return str(x[spec[1]]) < str(y[spec[1]])
			})
		}
		for _, capability := range arr(o["category_capabilities"]) {
			a := arr(obj(capability)["accepted_conditions"])
			sort.Slice(a, func(i, j int) bool { return str(a[i]) < str(a[j]) })
		}
	}
}
func setHashes(input object) {
	normalize(input)
	input["profile_snapshot_hash"] = digest(input["organisations"])
	input["input_hash"] = digest(selected(input, "contract_version", "batch", "rule_set", "evaluation_at", "organisations"))
}

func validateInput(input object) error {
	if err := validate("MatchingInput", input); err != nil {
		return err
	}
	if str(obj(input["rule_set"])["rule_set_version"]) != "binary-v1" {
		return fail("UNSUPPORTED_RULE_SET")
	}
	b := obj(input["batch"])
	if _, err := strconv.ParseUint(str(b["claim_epoch"]), 10, 64); err != nil {
		return fail("INVALID_CONTRACT")
	}
	w, err := cents(b["estimated_weight_kg"])
	if err != nil || w < 10 || w > 5000000 {
		return fail("INVALID_CONTRACT")
	}
	submitted, err := instant(b["submitted_at"])
	if err != nil {
		return err
	}
	deadline, err := instant(b["collection_deadline"])
	if err != nil {
		return err
	}
	if deadline.Before(submitted.Add(48*time.Hour)) || deadline.After(submitted.Add(90*24*time.Hour)) {
		return fail("INVALID_CONTRACT")
	}
	seen := map[string]bool{}
	orgIDs := map[string]bool{}
	for _, v := range arr(input["organisations"]) {
		o := obj(v)
		id := str(o["recycler_org_id"])
		if orgIDs[id] {
			return fail("INVALID_CONTRACT")
		}
		orgIDs[id] = true
		if p := obj(o["profile"]); p != nil {
			if n, e := strconv.ParseInt(str(p["version"]), 10, 64); e != nil || n < 1 {
				return fail("INVALID_CONTRACT")
			}
		}
		pools := map[string]bool{}
		for _, p := range arr(o["capacity_pools"]) {
			pools[str(obj(p)["id"])] = true
		}
		for _, spec := range [][2]string{{"capacity_pools", "pool_code"}, {"category_capabilities", "category"}, {"service_zones", "zone"}} {
			keys := map[string]bool{}
			for _, item := range arr(o[spec[0]]) {
				m := obj(item)
				id, key := str(m["id"]), str(m[spec[1]])
				if seen[id] || keys[key] {
					return fail("INVALID_CONTRACT")
				}
				seen[id] = true
				keys[key] = true
				if n, e := strconv.ParseInt(str(m["version"]), 10, 64); e != nil || n < 1 {
					return fail("INVALID_CONTRACT")
				}
				if spec[0] == "capacity_pools" {
					total, e1 := cents(m["total_kg"])
					reserved, e2 := cents(m["reserved_kg"])
					if e1 != nil || e2 != nil || reserved > total {
						return fail("INVALID_CONTRACT")
					}
				}
				if spec[0] == "category_capabilities" && !pools[str(m["capacity_pool_id"])] {
					return fail("INVALID_CONTRACT")
				}
			}
		}
	}
	normalize(input)
	if str(input["profile_snapshot_hash"]) != digest(input["organisations"]) || str(input["input_hash"]) != digest(selected(input, "contract_version", "batch", "rule_set", "evaluation_at", "organisations")) {
		return fail("INVALID_CONTRACT")
	}
	return nil
}

// expectedOutput recomputes all evidence with integer cents; it is a validator,
// never a remote call to Python and never a source of mutable matching inputs.
func expectedOutput(input object) (object, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	b := obj(input["batch"])
	evaluation, err := instant(input["evaluation_at"])
	if err != nil {
		return nil, err
	}
	deadline, _ := instant(b["collection_deadline"])
	weight, _ := cents(b["estimated_weight_kg"])
	candidates := make([]any, 0)
	eligible := 0
	for _, v := range arr(input["organisations"]) {
		o := obj(v)
		profile := obj(o["profile"])
		var capability, pool, zone object
		for _, v := range arr(o["category_capabilities"]) {
			if obj(v)["category"] == b["category"] {
				capability = obj(v)
			}
		}
		for _, v := range arr(o["capacity_pools"]) {
			if capability != nil && obj(v)["id"] == capability["capacity_pool_id"] {
				pool = obj(v)
			}
		}
		for _, v := range arr(o["service_zones"]) {
			if obj(v)["zone"] == b["zone"] {
				zone = obj(v)
			}
		}
		activeCap := capability != nil && boolean(capability["is_active"])
		activePool := pool != nil && boolean(pool["is_active"])
		activeZone := zone != nil && boolean(zone["is_active"])
		available := int64(0)
		var availableValue, feasibleValue, lead any
		if activePool {
			total, _ := cents(pool["total_kg"])
			reserved, _ := cents(pool["reserved_kg"])
			available = total - reserved
			availableValue = money(available)
		}
		var feasible time.Time
		if activeZone {
			minutes := number(zone["minimum_lead_minutes"])
			if minutes > math.MaxInt64/uint64(time.Minute) {
				return nil, fail("INVALID_CONTRACT")
			}
			feasible = evaluation.Add(time.Duration(minutes) * time.Minute)
			if feasible.Year() > 9999 {
				return nil, fail("INVALID_CONTRACT")
			}
			feasibleValue = stamp(feasible)
			lead = zone["minimum_lead_minutes"]
		}
		condition := false
		for _, v := range arr(capability["accepted_conditions"]) {
			condition = condition || v == b["condition_rating"]
		}
		flags := []bool{activeCap, profile != nil && boolean(profile["is_active"]) && activeCap && condition && (!boolean(b["is_data_bearing"]) || boolean(capability["supports_data_bearing"])), activePool && available >= weight, activeZone, activeZone && evaluation.Before(deadline) && !feasible.After(deadline)}
		capacityReason := "CAPACITY_UNAVAILABLE"
		if activePool {
			capacityReason = "INSUFFICIENT_CAPACITY"
		}
		reasons := []string{"CATEGORY_UNSUPPORTED", "CAPABILITY_UNSUPPORTED", capacityReason, "OUT_OF_SERVICE_ZONE", "DEADLINE_UNACHIEVABLE"}
		failures := make([]any, 0)
		for i, passed := range flags {
			if !passed {
				failures = append(failures, object{"rule_id": fmt.Sprintf("M%d", i+1), "reason_code": reasons[i]})
			}
		}
		reason := "ELIGIBLE"
		if len(failures) > 0 {
			reason = str(obj(failures[0])["reason_code"])
		} else {
			eligible++
		}
		missing := make([]any, 0)
		for _, x := range []struct {
			name string
			m    object
		}{{"PROFILE", profile}, {"CATEGORY_CAPABILITY", capability}, {"CAPACITY_POOL", pool}, {"SERVICE_ZONE", zone}} {
			if x.m == nil {
				missing = append(missing, x.name)
			}
		}
		candidate := object{"recycler_org_id": o["recycler_org_id"], "profile_version": profile["version"], "is_matched": len(failures) == 0, "available_capacity_kg": availableValue, "capacity_pool_id": pool["id"], "capacity_version": pool["version"], "minimum_lead_minutes": lead, "feasible_at": feasibleValue, "reason_code": reason, "failed_rules_json": failures, "evidence_json": object{"capability_id": capability["id"], "capability_version": capability["version"], "service_zone_id": zone["id"], "service_zone_version": zone["version"], "missing_configuration": missing}}
		for i, k := range []string{"category_match", "capability_match", "capacity_available", "zone_match", "deadline_viable"} {
			candidate[k] = flags[i]
		}
		candidates = append(candidates, candidate)
	}
	out := selected(input, "contract_version", "run_id", "decision_id", "context_generation", "input_hash", "profile_snapshot_hash", "evaluation_at")
	for _, k := range []string{"batch_id", "batch_version", "claim_epoch"} {
		out[k] = b[k]
	}
	out["rule_set_id"] = obj(input["rule_set"])["rule_set_id"]
	out["candidates"] = candidates
	out["evaluated_count"] = len(candidates)
	out["eligible_count"] = eligible
	out["outcome"] = "NO_MATCH"
	out["primary_reason"] = "NO_APPROVED_ORGANISATION"
	if len(candidates) > 0 {
		out["primary_reason"] = "NO_ELIGIBLE_ORGANISATION"
	}
	if eligible > 0 {
		out["outcome"] = "MATCHED"
		out["primary_reason"] = "ELIGIBLE_EXISTS"
	}
	return out, validate("MatchingOutput", out)
}

type contractError string

func (e contractError) Error() string { return string(e) }
func fail(code string) error          { return contractError(code) }
