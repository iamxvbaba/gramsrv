UPDATE sticker_sets
SET installed = false,
    installed_date = 0
WHERE installed = true
   OR installed_date <> 0;
