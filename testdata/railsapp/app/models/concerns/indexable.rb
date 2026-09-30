module Indexable
  extend ActiveSupport::Concern

  included do
    has_many :index_entries, dependent: :destroy
    after_create_commit :reindex
  end

  def reindex
    SearchEntry.upsert!(self)
  end
end
