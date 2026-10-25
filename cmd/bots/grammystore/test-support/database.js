import pg from "pg";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { BotDatabase } from "../src/db.js";

export async function openTestDatabase(options = {}) {
  const schema = `bot_test_${randomUUID().replaceAll("-", "")}`;
  const admin = new pg.Pool({ connectionString: process.env.DATABASE_URL, max: 1 });
  const url = new URL(process.env.DATABASE_URL);
  url.searchParams.set("options", `-c search_path=${schema}`);
  const db = new BotDatabase(url.toString(), options);
  const close = db.close.bind(db);
  db.close = async () => {
    await close();
    // Only the schema created by this fixture is removed; never shared tables.
    try { await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`); }
    finally { await admin.end(); }
  };
  try {
    await admin.query(`CREATE SCHEMA ${schema}`);
    await db.pool.query(readFileSync(new URL("../db/init.sql", import.meta.url), "utf8"));
    return db;
  } catch (error) {
    await db.close();
    throw error;
  }
}
