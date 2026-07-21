import { CoreApp, DataSourceInstanceSettings, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';
import { DEFAULT_QUERY, ParquetBlobDataSourceOptions, ParquetBlobQuery } from './types';

export class DataSource extends DataSourceWithBackend<ParquetBlobQuery, ParquetBlobDataSourceOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<ParquetBlobDataSourceOptions>) {
    super(instanceSettings);
  }

  getDefaultQuery(_: CoreApp): Partial<ParquetBlobQuery> {
    return DEFAULT_QUERY;
  }

  // Dashboard template variables are expanded in the browser, before the
  // query reaches the backend (the backend never sees $variables).
  applyTemplateVariables(query: ParquetBlobQuery, scopedVars: ScopedVars): ParquetBlobQuery {
    return {
      ...query,
      rawSql: getTemplateSrv().replace(query.rawSql ?? '', scopedVars),
    };
  }

  filterQuery(query: ParquetBlobQuery): boolean {
    return !!query.rawSql?.trim();
  }
}
