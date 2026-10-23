-- Move stored freeze-appeal links onto the ShuzaGram public link host (sgq.me).
UPDATE account_restrictions
SET appeal_url = regexp_replace(appeal_url, '^https?://(?:t|telegram)[.](?:me|dog)/', 'https://sgq.me/', 'i')
WHERE appeal_url ~* '^https?://(?:t|telegram)[.](?:me|dog)/';
