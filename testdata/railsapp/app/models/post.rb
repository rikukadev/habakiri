class Post < ApplicationRecord
  belongs_to :user
  belongs_to :category, optional: true
  has_many :comments, as: :commentable
  after_save :sync_search_index

  private

  def sync_search_index
    SearchEntry.upsert!(self)
  end
end
