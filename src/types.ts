import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryFormat = 'table' | 'timeseries';

export interface ParquetBlobQuery extends DataQuery {
  rawSql?: string;
  format?: QueryFormat;
}

export const DEFAULT_QUERY: Partial<ParquetBlobQuery> = {
  rawSql: '',
  format: 'table',
};

export interface ParquetBlobDataSourceOptions extends DataSourceJsonData {
  accountName?: string;
  maxRows?: number;
}

export interface ParquetBlobSecureJsonData {
  connectionString?: string;
}
