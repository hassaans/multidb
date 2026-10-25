// A thin typed wrapper around multidb's SQL functions for supabase-js v2.
// Copy into your project; it has no dependencies beyond @supabase/supabase-js.

import type { SupabaseClient } from '@supabase/supabase-js'

export type FieldType =
  | 'text' | 'number' | 'boolean' | 'date' | 'datetime' | 'json' | 'relation' | 'relation_many'

export interface QuerySpec {
  v?: 1
  collection: string
  filter?: Record<string, unknown>
  sort?: { field: string; dir?: 'asc' | 'desc' }[]
  limit?: number
  cursor?: string
  expand?: string[]
  groupBy?: string[]
  aggregate?: Record<string, Partial<Record<'sum' | 'avg' | 'min' | 'max' | 'count' | 'countDistinct', string>>>
}

export interface QueryResult<Row = Record<string, unknown>> {
  rows: Row[]
  cursor: string | null
  stats: { ms: number }
}

export class MultiDB {
  constructor(private readonly supabase: SupabaseClient, private readonly schema = 'multidb') {}

  private get db() {
    return this.supabase.schema(this.schema)
  }

  private async rpc<T>(fn: string, args: Record<string, unknown>): Promise<T> {
    const { data, error } = await this.db.rpc(fn, args)
    if (error) throw Object.assign(new Error(error.message), { code: error.code })
    return data as T
  }

  async createDatabase(userId: string, name: string) {
    const { data, error } = await this.db.from('databases').insert({ tenant_id: userId, name }).select().single()
    if (error) throw error
    return data as { id: string; name: string }
  }

  async createCollection(userId: string, databaseId: string, name: string) {
    const { data, error } = await this.db
      .from('collections').insert({ database_id: databaseId, tenant_id: userId, name }).select().single()
    if (error) throw error
    return data as { id: string; name: string }
  }

  async addField(userId: string, collectionId: string, name: string, type: FieldType, options: Record<string, unknown> = {}) {
    const { error } = await this.db
      .from('fields').insert({ collection_id: collectionId, tenant_id: userId, name, type, options })
    if (error) throw error
  }

  insert(collectionId: string, docs: Record<string, unknown>[]) {
    return this.rpc<string[]>('insert_records', { cid: collectionId, docs })
  }

  update(collectionId: string, recordId: string, patch: Record<string, unknown>) {
    return this.rpc<void>('update_record', { cid: collectionId, rid: recordId, patch })
  }

  delete(collectionId: string, recordIds: string[]) {
    return this.rpc<number>('delete_records', { cid: collectionId, rids: recordIds })
  }

  query<Row = Record<string, unknown>>(spec: QuerySpec) {
    return this.rpc<QueryResult<Row>>('query', { spec: { v: 1, ...spec } })
  }

  /** Iterate every page of a query. */
  async *pages<Row = Record<string, unknown>>(spec: QuerySpec) {
    let cursor: string | undefined
    do {
      const page = await this.query<Row>({ ...spec, cursor })
      yield page.rows
      cursor = page.cursor ?? undefined
    } while (cursor)
  }
}

// Usage:
//   const mdb = new MultiDB(supabase)
//   const shop = await mdb.createDatabase(user.id, 'shop')
//   const orders = await mdb.createCollection(user.id, shop.id, 'orders')
//   await mdb.addField(user.id, orders.id, 'amount', 'number')
//   await mdb.insert(orders.id, [{ amount: 120 }])
//   const { rows } = await mdb.query({ collection: orders.id, filter: { amount: { $gte: 100 } } })
