-- 1935 the dashboard reads a person's daily rows across all their projects and years: one row per project per day, summed
-- by day. With the figures in the index the read is an index scan and never one lookup in the table for each row (five years
-- of 20 projects is 36,520 of them). The cost is one index entry rewritten with the day's row on each save.
CREATE INDEX daily_stats_studio ON daily_stats(user_id, project_id, day, words_added, words_net, active_seconds, sessions, goal_hit);
