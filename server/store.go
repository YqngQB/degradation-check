package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Result 是某个账号某项测试的最新结果（每个账号每类只保留一条）。
type Result struct {
	AccountID       int64           `json:"account_id"`
	TestType        string          `json:"test_type"`
	AccountName     string          `json:"account_name"`
	AccountEmail    string          `json:"account_email"`
	AccountPlatform string          `json:"account_platform"`
	AccountType     string          `json:"account_type"`
	Model           string          `json:"model"`
	Effort          string          `json:"effort"`
	Variant         string          `json:"variant"`
	Status          string          `json:"status"`
	Error           string          `json:"error"`
	Payload         json.RawMessage `json:"payload"`
	DurationMS      int64           `json:"duration_ms"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	ReasoningTokens int64           `json:"reasoning_tokens"`
	TotalRuns       int             `json:"total_runs"`
	CorrectRuns     int             `json:"correct_runs"`
	TestedAt        time.Time       `json:"tested_at"`
}

type Store struct {
	db *sql.DB
}

// Account IDs/results are meaningful only within one Sub2API installation.
func (s *Store) bindInstance(base string) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS instance_binding (id INTEGER PRIMARY KEY CHECK(id=1), base TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO instance_binding (id,base) VALUES (1,?)`, base); err != nil {
		return err
	}
	var bound string
	if err := s.db.QueryRow(`SELECT base FROM instance_binding WHERE id=1`).Scan(&bound); err != nil {
		return err
	}
	if bound != base {
		return fmt.Errorf("此数据目录已绑定其他 Sub2API 地址；迁移域名时请先备份并确认实例，独立站点请使用独立 DATA_DIR")
	}
	return nil
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS results (
	account_id       INTEGER NOT NULL,
	test_type        TEXT    NOT NULL,
	account_name     TEXT    NOT NULL DEFAULT '',
	account_email    TEXT    NOT NULL DEFAULT '',
	account_platform TEXT    NOT NULL DEFAULT '',
	account_type     TEXT    NOT NULL DEFAULT '',
	model            TEXT    NOT NULL DEFAULT '',
	effort           TEXT    NOT NULL DEFAULT '',
	variant          TEXT    NOT NULL DEFAULT '',
	status           TEXT    NOT NULL DEFAULT '',
	error            TEXT    NOT NULL DEFAULT '',
	payload          TEXT    NOT NULL DEFAULT '{}',
	duration_ms      INTEGER NOT NULL DEFAULT 0,
	input_tokens     INTEGER NOT NULL DEFAULT 0,
	output_tokens    INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	total_runs       INTEGER NOT NULL DEFAULT 0,
	correct_runs     INTEGER NOT NULL DEFAULT 0,
	tested_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (account_id, test_type)
);`)
	return err
}

// Upsert 覆盖写：只留每个账号每类测试的最新一条，累计计数继续累加。
func (s *Store) Upsert(r *Result) error {
	payload := string(r.Payload)
	if payload == "" {
		payload = "{}"
	}
	totalDelta, correctDelta := 1, 0
	if r.Status == StatusSkipped {
		totalDelta = 0
	}
	if r.Status == StatusCorrect {
		correctDelta = 1
	}
	_, err := s.db.Exec(`
INSERT INTO results (account_id, test_type, account_name, account_email, account_platform, account_type,
	model, effort, variant, status, error, payload, duration_ms,
	input_tokens, output_tokens, reasoning_tokens, total_runs, correct_runs, tested_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(account_id, test_type) DO UPDATE SET
	account_name=excluded.account_name,
	account_email=excluded.account_email,
	account_platform=excluded.account_platform,
	account_type=excluded.account_type,
	model=excluded.model,
	effort=excluded.effort,
	variant=excluded.variant,
	status=excluded.status,
	error=excluded.error,
	payload=excluded.payload,
	duration_ms=excluded.duration_ms,
	input_tokens=excluded.input_tokens,
	output_tokens=excluded.output_tokens,
	reasoning_tokens=excluded.reasoning_tokens,
	total_runs=results.total_runs + ?,
	correct_runs=results.correct_runs + ?,
	tested_at=excluded.tested_at`,
		r.AccountID, r.TestType, r.AccountName, r.AccountEmail, r.AccountPlatform, r.AccountType,
		r.Model, r.Effort, r.Variant, r.Status, r.Error, payload, r.DurationMS,
		r.InputTokens, r.OutputTokens, r.ReasoningTokens, totalDelta, correctDelta, r.TestedAt,
		totalDelta, correctDelta)
	if err != nil {
		return fmt.Errorf("写入结果失败: %w", err)
	}
	return nil
}

func (s *Store) List() ([]*Result, error) {
	rows, err := s.db.Query(`
SELECT account_id, test_type, account_name, account_email, account_platform, account_type,
       model, effort, variant, status, error, payload, duration_ms,
       input_tokens, output_tokens, reasoning_tokens, total_runs, correct_runs, tested_at
FROM results ORDER BY tested_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Result, 0, 64)
	for rows.Next() {
		var (
			r       Result
			payload string
		)
		if err := rows.Scan(&r.AccountID, &r.TestType, &r.AccountName, &r.AccountEmail,
			&r.AccountPlatform, &r.AccountType, &r.Model, &r.Effort, &r.Variant,
			&r.Status, &r.Error, &payload, &r.DurationMS, &r.InputTokens,
			&r.OutputTokens, &r.ReasoningTokens, &r.TotalRuns, &r.CorrectRuns, &r.TestedAt); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) Clear(testType string) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if testType == "" {
		res, err = s.db.Exec(`DELETE FROM results`)
	} else {
		res, err = s.db.Exec(`DELETE FROM results WHERE test_type = ?`, testType)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Latest 取某个账号某项测试的最新一条（供预览使用）。
func (s *Store) Latest(accountID int64, testType string) (*Result, error) {
	var (
		r       Result
		payload string
	)
	err := s.db.QueryRow(`
SELECT account_id, test_type, account_name, account_email, account_platform, account_type,
       model, effort, variant, status, error, payload, duration_ms,
       input_tokens, output_tokens, reasoning_tokens, total_runs, correct_runs, tested_at
FROM results WHERE account_id=? AND test_type=?`, accountID, testType).
		Scan(&r.AccountID, &r.TestType, &r.AccountName, &r.AccountEmail, &r.AccountPlatform,
			&r.AccountType, &r.Model, &r.Effort, &r.Variant, &r.Status, &r.Error, &payload,
			&r.DurationMS, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens,
			&r.TotalRuns, &r.CorrectRuns, &r.TestedAt)
	if err != nil {
		return nil, err
	}
	r.Payload = json.RawMessage(payload)
	return &r, nil
}
