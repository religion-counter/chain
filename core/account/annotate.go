package account

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"chain/database/pg"
	"chain/errors"
)

// AnnotateTxs adds account data to transactions
func AnnotateTxs(ctx context.Context, txs []map[string]interface{}) error {
	controlMaps := make(map[string][]map[string]interface{})
	outputMaps := make(map[string][]map[string]interface{})
	var controlPrograms [][]byte

	update := func(s interface{}, maps ...map[string][]map[string]interface{}) error {
		asSlice, ok := s.([]interface{})
		if !ok {
			return errors.Wrap(fmt.Errorf("expected slice, got %T", s))
		}
		for _, m := range asSlice {
			asMap, ok := m.(map[string]interface{})
			if !ok {
				return errors.Wrap(fmt.Errorf("expected map, got %T", m))
			}
			if asMap["control_program"] == nil {
				// Issuance inputs don't have control_programs
				continue
			}
			controlString, ok := asMap["control_program"].(string)
			if !ok {
				return errors.Wrap(fmt.Errorf("expected string, got %T", asMap["control_program"]))
			}
			controlProgram, err := hex.DecodeString(controlString)
			if err != nil {
				return err
			}
			controlPrograms = append(controlPrograms, controlProgram)
			for _, m := range maps {
				m[string(controlProgram)] = append(m[string(controlProgram)], asMap)
			}
		}
		return nil
	}

	for _, tx := range txs {
		err := update(tx["outputs"], controlMaps, outputMaps)
		if err != nil {
			return err
		}
		err = update(tx["inputs"], controlMaps)
		if err != nil {
			return err
		}
	}

	const q = `
		SELECT signer_id, control_program, change, alias, tags
		FROM account_control_programs
		LEFT JOIN signers ON signers.id=account_control_programs.signer_id
		LEFT JOIN accounts ON accounts.account_id=signers.id
		WHERE control_program=ANY($1::bytea[])
	`
	var (
		ids         []string
		programs    [][]byte
		changeFlags []bool
		aliases     []sql.NullString
		tags        []*json.RawMessage
	)
	err := pg.ForQueryRows(ctx, q, pq.ByteaArray(controlPrograms), func(accountID string, program []byte, change bool, alias sql.NullString, accountTags []byte) {
		ids = append(ids, accountID)
		programs = append(programs, program)
		changeFlags = append(changeFlags, change)
		aliases = append(aliases, alias)
		if len(accountTags) > 0 {
			tags = append(tags, (*json.RawMessage)(&accountTags))
		} else {
			tags = append(tags, nil)
		}
	})
	if err != nil {
		return err
	}
	for i := range ids {
		maps := controlMaps[string(programs[i])]
		for _, m := range maps {
			m["account_id"] = ids[i]
			if tags[i] != nil {
				m["account_tags"] = tags[i]
			}
			if aliases[i].Valid {
				m["account_alias"] = aliases[i].String
			}
		}

		// Add output-only annotations.
		outs := outputMaps[string(programs[i])]
		for _, out := range outs {
			if changeFlags[i] {
				out["purpose"] = "change"
			} else {
				out["purpose"] = "receive"
			}
		}
	}

	return nil
}
