-- 0006: one review per reviewer per pull request.
--
-- Defense in depth for branch protection: ApprovalState counts approvals
-- from review rows, so stacked rows from the same reviewer could inflate
-- approval counts. The service layer replaces a reviewer's previous
-- verdict via upsert; this index makes the invariant explicit in the
-- schema. The author self-review block lives in the prservice layer
-- (verdicts only, comments still allowed).

-- Keep the newest row per (pull_id, reviewer_id) before adding the index.
DELETE FROM pull_request_reviews
 WHERE id NOT IN (
	SELECT MAX(id) FROM pull_request_reviews GROUP BY pull_id, reviewer_id
);

CREATE UNIQUE INDEX idx_pr_reviews_pull_reviewer
	ON pull_request_reviews(pull_id, reviewer_id);
