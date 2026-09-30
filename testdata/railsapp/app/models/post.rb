class Post < ApplicationRecord
  belongs_to :user
  belongs_to :category, optional: true
  has_many :comments, as: :commentable
  with_options class_name: "User", optional: true do
    belongs_to :editor, foreign_key: "edited_by_id"
  end

  after_save :sync_search_index

  private

  def sync_search_index
    SearchEntry.upsert!(self)
  end
end
